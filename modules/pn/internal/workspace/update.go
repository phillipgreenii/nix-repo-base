package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// ulLibResolverRef is the flake app that prints the update-locks lib dir. It
// mirrors the one-liner each update-locks.sh uses; pn resolves it once per run
// and injects the result so the per-repo scripts skip the (remote) evaluation.
const ulLibResolverRef = "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir"

// UpdateScope chooses how much `pn workspace update` relocks (ADR 0045).
type UpdateScope string

const (
	// ScopeSiblings relocks only the workspace-sibling flake inputs and skips
	// each repo's update-locks.sh.
	ScopeSiblings UpdateScope = "siblings"
	// ScopeFlake relocks every flake input (siblings, nixpkgs, third-party) with
	// one `nix flake update --refresh` and skips update-locks.sh.
	ScopeFlake UpdateScope = "flake"
	// ScopeFull runs update-locks.sh, then relocks every flake input. It is the
	// default.
	ScopeFull UpdateScope = "full"
)

// ParseUpdateScope parses a --scope value. The empty string is the unset flag
// and means ScopeFull; callers that need to reject an explicit empty value
// check that themselves.
func ParseUpdateScope(s string) (UpdateScope, error) {
	switch UpdateScope(s) {
	case "":
		return ScopeFull, nil
	case ScopeSiblings, ScopeFlake, ScopeFull:
		return UpdateScope(s), nil
	}
	return "", fmt.Errorf("unknown scope %q (want siblings, flake or full)", s)
}

// scopeSkipBanner returns the "skipping update-locks.sh" line for repo name in
// the siblings and flake scopes, worded for whether the relock actually moved
// anything. Both flows print it so the wording stays consistent.
func scopeSkipBanner(name string, scope UpdateScope, relocked bool) string {
	if scope == ScopeFlake {
		inputs := "no flake input moved"
		if relocked {
			inputs = "flake inputs relocked"
		}
		return fmt.Sprintf("  ⊘ %s: scope flake — skipping update-locks.sh (%s, per-package locks untouched)\n", name, inputs)
	}
	inputs := "no workspace inputs to relock"
	if relocked {
		inputs = "workspace inputs relocked"
	}
	return fmt.Sprintf("  ⊘ %s: scope siblings — skipping update-locks.sh (%s, nixpkgs/third-party untouched)\n", name, inputs)
}

// relockOutcome classifies a failed all-inputs relock for a scope.
type relockOutcome int

const (
	relockWarn      relockOutcome = iota // full scope: keep update-locks.sh commits, warn
	relockFailRepo                       // the repo fails at relock-flake
	relockAbortRun                       // out of disk: the whole run stops
	relockInterrupt                      // context cancelled: the repo and the run end
)

// classifyRelockErr decides what a relockAllInputs error means. A context
// interrupt and a full disk win over everything; a failed lock restore is a
// repo failure in every scope; otherwise full scope warns and flake fails.
func classifyRelockErr(ctx context.Context, scope UpdateScope, err error) relockOutcome {
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return relockInterrupt
	case errors.Is(err, errRelockNoSpace):
		return relockAbortRun
	case errors.Is(err, errRelockRestore):
		return relockFailRepo
	case scope == ScopeFull:
		return relockWarn
	}
	return relockFailRepo
}

// relockFlakeInputs runs the all-inputs relock for one repo with its pn.relock
// span and the started/finished progress lines.
func (ws *Workspace) relockFlakeInputs(ctx context.Context, out io.Writer, name, dir string) (int, error) {
	ctx, end := telemetry.StartRelock(ctx, "flake")
	start := time.Now()
	fmt.Fprintf(out, "    [%s] relock all flake inputs (nix flake update --refresh): started\n", name)
	changed, err := ws.relockAllInputs(ctx, out, name, dir, ws.resolveFlakePath(name))
	phaseOutcome := "ok"
	if err != nil {
		phaseOutcome = "failed"
	}
	fmt.Fprintf(out, "    [%s] relock all flake inputs (nix flake update --refresh): finished, %s (%s)\n", name, phaseOutcome, elapsedSecs(start))
	end(changed, err)
	return changed, err
}

// relockWarning is the line printed when a full-scope relock fails after
// update-locks.sh succeeded.
func relockWarning(name string, err error) string {
	return fmt.Sprintf("  ⚠ %s: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved: %v\n", name, err)
}

// UpdateOptions configures Update.
type UpdateOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
	// Recreate forces full lock recreation (currently treated as an
	// indicator for Upgrade; see upgrade.go).
	Recreate bool
	// InPlace selects the legacy direct-on-main flow (pull → update-locks →
	// push in each primary checkout). When false (the default), Update isolates
	// each repo in an ephemeral worktree and fast-forwards back to main.
	InPlace bool
	// Scope chooses how much is relocked (ADR 0045); the zero value means
	// ScopeFull. Like plain Update every scope is LOCAL-ONLY: it resolves
	// inputs against their DECLARED REMOTE URLs and commits the bump, and it
	// never pushes (ADR 0023). Topological order, worktree isolation vs InPlace,
	// pull/rebase and local integration are the same in every scope. Convergence
	// onto a rev that is only LOCAL is impossible by construction (C1), so
	// cross-repo convergence after a local land is `pn workspace push`'s job.
	Scope UpdateScope
	// ULLibDir, when set, is exported as UL_LIB_DIR to each update-locks.sh so
	// it skips its own determine-ul-lib-dir resolution. Resolve it once per run
	// via ResolveULLibDir. Empty leaves each script to resolve for itself.
	ULLibDir string
	// Log, when non-nil, receives a structured JSONL event stream for the run
	// (run_start / project_result / run_end). Nil disables event logging.
	Log *eventlog.Writer
}

// scope returns the effective scope: the zero value means ScopeFull.
func (o UpdateOptions) scope() UpdateScope {
	if o.Scope == "" {
		return ScopeFull
	}
	return o.Scope
}

// ResolveULLibDir runs the update-locks lib resolver once and returns the path
// it prints (with WORKSPACE_ROOT set so its on-disk sibling tier can fire).
// Best-effort: any failure returns "" so callers fall back to the per-repo
// resolution baked into each update-locks.sh.
func (ws *Workspace) ResolveULLibDir(ctx context.Context) string {
	res, err := ws.runner.Run(ctx, "nix", []string{"run", ulLibResolverRef},
		exec.RunOptions{Env: map[string]string{"WORKSPACE_ROOT": ws.root}})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// ulSubprocessEnv builds the env for an update-locks.sh invocation: the
// workspace-root markers plus UL_LIB_DIR when one was resolved.
func (ws *Workspace) ulSubprocessEnv(ulLibDir string) map[string]string {
	env := map[string]string{
		"PN_WORKSPACE_ROOT": ws.root,
		"WORKSPACE_ROOT":    ws.root,
	}
	if ulLibDir != "" {
		env["UL_LIB_DIR"] = ulLibDir
	}
	return env
}

// Update runs the workspace update. By default each repo is updated in an
// ephemeral git worktree and fast-forwarded back onto the primary main
// (updateViaWorktree); opts.InPlace selects the legacy direct-on-main flow
// (updateInPlace). See ADR 0009.
func (ws *Workspace) Update(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	telemetry.RecordUpdateScope(ctx, string(opts.scope()))
	if opts.InPlace {
		return ws.updateInPlace(ctx, out, opts)
	}
	return ws.updateViaWorktree(ctx, out, opts)
}

// updateInPlace pulls each workspace repo and relocks it per opts.Scope: siblings
// relocks just the workspace-sibling inputs, flake relocks every flake input, and
// full runs ./update-locks.sh and then relocks every flake input. It is LOCAL-ONLY: it
// pulls, but it does NOT push — publishing is `pn workspace push` (ADR 0023).
// Repos without an upstream skip the pull but still attempt the relock. Repos
// with a dirty working tree are skipped (non-fatal).
//
// Per-repo failures are aggregated rather than aborting the whole sweep on the
// first error: every repo is attempted and the failing repos are named in the
// returned error at the end (like FlakeCheck). Within a single repo, a failed
// pull marks it failed and skips the relock (the working tree is suspect).
//
// The provided context is checked between repos and between sub-steps; a
// cancelled context aborts cleanly with the next ctx.Err() observed.
//
// Repos are processed in topological order (dependencies before consumers;
// foundation repos first, see ADR-0033). Note
// that order no longer buys cross-repo convergence here: with no push, an
// upstream's fresh commits are not on its remote, and a consumer can only relock
// to a rev that IS on the remote (C1). The order is kept because it is the
// workspace's canonical iteration order and because `pn workspace push` — which
// does converge — relies on the same ordering (including foundation-first).
// updateInPlace is a required-terminal command: it errors when no terminal is configured.
func (ws *Workspace) updateInPlace(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if _, err := ws.requireTerminal(ctx, opts.Terminal); err != nil {
		return err
	}
	names := ws.topoAlpha(ctx)
	scope := opts.scope()
	// Only the siblings scope reads the sibling aliases; the other scopes relock
	// every input through nix with no sibling special-casing (ADR 0023 item 1), so
	// deriving the lock for them would cost a nix eval per repo and be unused.
	// When derived, effectiveLock (not the possibly-empty ws.lock) is the source
	// topoAlpha trusts (C3).
	var edgeLock *Lock
	if scope == ScopeSiblings {
		edgeLock, _, _ = ws.effectiveLock(ctx)
	}
	_ = opts.Log.Emit("info", "run_start", "workspace update started", map[string]any{
		"terminal": opts.Terminal,
		"projects": len(names),
		"scope":    string(scope),
		"root":     ws.root,
	})

	var failed []string
	var aborted bool
	var abortedName, abortStep string
	first := true
	for idx, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		repoDir := filepath.Join(ws.root, name)
		// pn.repo span for this repo's whole update; every exit below ends it.
		ctx, endRepo := telemetry.StartRepo(ctx, name)

		// Blank line between repo blocks (not before the first).
		if !first {
			fmt.Fprintln(out)
		}
		first = false

		// Progress: a start line with position N/M and (finishRepo) a finish line
		// with outcome and elapsed seconds.
		repoStart := time.Now()
		fmt.Fprintf(out, "  [%d/%d] %s: starting\n", idx+1, len(names), name)
		finishRepo := func(outcome string) {
			fmt.Fprintf(out, "  [%d/%d] %s: %s (%s)\n", idx+1, len(names), name, outcome, elapsedSecs(repoStart))
		}

		// Skip (non-fatal) if the working tree is dirty (modified or staged), or
		// if cleanliness could not be determined — either way updating is unsafe.
		dirty, dirtyErr := ws.isDirty(ctx, repoDir)
		if dirtyErr != nil {
			fmt.Fprintf(out, "  ⊘ skipping %s — could not check working tree: %v\n", name, dirtyErr)
			_ = opts.Log.Emit("warn", "project_result", "project skipped (dirty-check failed)",
				map[string]any{"name": name, "outcome": "skipped", "error": dirtyErr.Error()})
			finishRepo("skipped")
			endRepo(dirtyErr)
			continue
		}
		if dirty {
			fmt.Fprintf(out, "  ⊘ skipping %s — working tree has uncommitted changes\n", name)
			_ = opts.Log.Emit("warn", "project_result", "project skipped (dirty working tree)",
				map[string]any{"name": name, "outcome": "skipped"})
			finishRepo("skipped")
			endRepo(nil)
			continue
		}

		fmt.Fprintf(out, "  --== update %s ==--  \n", name)
		hasUp := ws.hasUpstream(ctx, repoDir)
		pullFailed := false
		relocked := false
		projectFailed := false
		transient := 0
		var ulSteps []telemetry.ULStep

		if hasUp {
			if err := ctx.Err(); err != nil {
				endRepo(err)
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				pullFailed = true
				projectFailed = true
			}
		}
		// Relock per scope (ADR 0045); nothing here pushes (ADR 0023):
		//   siblings: just the workspace-sibling inputs, from their declared remotes.
		//   flake:    every flake input via one `nix flake update --refresh`.
		//   full:     update-locks.sh, then (unless it failed or aborted) every flake
		//             input. A repo without the script skips straight to the relock.
		// Skipped entirely if the pull failed: the working tree is suspect.
		failStep := ""
		relockWarned := false
		var interruptErr error
		if !pullFailed {
			if err := ctx.Err(); err != nil {
				endRepo(err)
				return fmt.Errorf("update interrupted: %w", err)
			}
			if scope == ScopeSiblings {
				phaseStart := time.Now()
				fmt.Fprintf(out, "    [%s] nix flake update (siblings): started\n", name)
				sctx, endRelock := telemetry.StartRelock(ctx, "siblings")
				did, err := ws.propagateWorkspaceEdges(sctx, out, name, repoDir, ws.resolveFlakePath(name), workspaceAliasesFromLock(edgeLock, name))
				endRelock(boolToInt(did), err)
				fmt.Fprintf(out, "    [%s] nix flake update (siblings): finished (%s)\n", name, elapsedSecs(phaseStart))
				if err != nil {
					fmt.Fprintf(out, "  ✗ %s: relock-siblings failed: %v\n", name, err)
					projectFailed = true
					failStep = "relock-siblings"
				} else {
					relocked = did
					fmt.Fprint(out, scopeSkipBanner(name, scope, relocked))
				}
			}
			if scope == ScopeFull {
				if fileExists(filepath.Join(repoDir, "update-locks.sh")) {
					phaseStart := time.Now()
					fmt.Fprintf(out, "    [%s] update-locks.sh (runs nix flake update): started\n", name)
					res, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir, Env: ws.ulSubprocessEnv(opts.ULLibDir), Stdout: out, Stderr: out, PropagateTrace: true})
					// res.Stdout is populated on both success and CommandError, so the
					// transient count crosses the boundary even for a hard-failed repo
					// (ul_finalize prints UL_RESULT before its non-zero exit).
					transient = parseULTransient(res.Stdout)
					ulSteps = parseULSteps(res.Stdout)
					telemetry.RecordULSteps(ctx, ulSteps)
					phaseOutcome := "ok"
					if err != nil {
						phaseOutcome = "failed"
					}
					fmt.Fprintf(out, "    [%s] update-locks.sh (runs nix flake update): finished, %s (%s)\n", name, phaseOutcome, elapsedSecs(phaseStart))
					if err != nil {
						var cmdErr *exec.CommandError
						if errors.As(err, &cmdErr) && cmdErr.Result.ExitCode == ulExitAbort {
							aborted, abortedName, abortStep = true, name, "update-locks"
							fmt.Fprintf(out, "  ⛔ %s: environmental/resource failure — aborting run\n", name)
						} else {
							projectFailed = true
							failStep = "update-locks"
						}
					}
				} else {
					fmt.Fprintf(out, "  ⊘ %s: no update-locks.sh — skipping its per-package steps (flake inputs relocked next)\n", name)
				}
			}
			if (scope == ScopeFlake || scope == ScopeFull) && !projectFailed && !aborted {
				changed, err := ws.relockFlakeInputs(ctx, out, name, repoDir)
				if err != nil {
					switch classifyRelockErr(ctx, scope, err) {
					case relockWarn:
						relockWarned = true
						fmt.Fprint(out, relockWarning(name, err))
					case relockAbortRun:
						aborted, abortedName, abortStep = true, name, "relock-flake"
						fmt.Fprintf(out, "  ✗ %s: relock-flake failed: %v\n", name, err)
						fmt.Fprintf(out, "  ⛔ %s: environmental/resource failure — aborting run\n", name)
					case relockInterrupt:
						interruptErr = err
						projectFailed, failStep = true, "relock-flake"
						fmt.Fprintf(out, "  ✗ %s: relock-flake interrupted: %v\n", name, err)
					default:
						projectFailed, failStep = true, "relock-flake"
						fmt.Fprintf(out, "  ✗ %s: relock-flake failed: %v\n", name, err)
					}
				} else if scope == ScopeFlake {
					fmt.Fprint(out, scopeSkipBanner(name, scope, changed > 0))
				}
			}
		}
		// An environmental/resource abort applies to every remaining repo: record
		// it and stop the rest of the loop.
		if aborted {
			failed = append(failed, name)
			_ = opts.Log.Emit("error", "project_result", "project aborted", withFields(map[string]any{
				"name": name, "outcome": "aborted", "failed_step": abortStep, "transient": transient,
			}, ulStepFields(ulSteps)))
			finishRepo("failed")
			fmt.Fprintf(out, "  ✗ %s: update FAILED (aborted at %s: environmental/resource failure)\n", name, abortStep)
			endRepo(errors.New("update aborted: environmental/resource failure"))
			break
		}
		// NO PUSH. update leaves every commit local; `pn workspace push` publishes
		// and is the only thing that propagates sibling locks (ADR 0023).

		if projectFailed {
			failed = append(failed, name)
			step := failStep
			if pullFailed {
				step = "pull"
			}
			_ = opts.Log.Emit("error", "project_result", "project failed", withFields(map[string]any{
				"name": name, "outcome": "failed", "failed_step": step, "transient": transient,
			}, ulStepFields(ulSteps)))
			finishRepo("failed")
			fmt.Fprintf(out, "  ✗ %s: update FAILED at step %s\n", name, step)
			endRepo(fmt.Errorf("update failed at %s", step))
		} else {
			// A green repo with transient steps, or one whose full-scope relock
			// failed after update-locks.sh succeeded, is escalated to warn: the
			// exit code alone would never surface a silently skipped update (ADR 0020).
			level, msg := "info", "project updated"
			fields := withFields(map[string]any{"name": name, "outcome": "ok", "transient": transient}, ulStepFields(ulSteps))
			if transient > 0 {
				level = "warn"
				msg = fmt.Sprintf("project updated, but %d transient step(s) were skipped this run — a permanently-transient step keeps the run green while an update is silently skipped (ADR 0020)", transient)
			}
			if relockWarned {
				level = "warn"
				msg += "; the all-inputs relock failed after one retry, so flake inputs were not moved"
				fields["relock_failed"] = true
			}
			_ = opts.Log.Emit(level, "project_result", msg, fields)
			if relockWarned {
				finishRepo("ok, relock failed")
			} else {
				finishRepo("ok")
			}
			endRepo(nil)
		}
		// A cancel during the repo's last step must end the run here, not carry on.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		if interruptErr != nil {
			return fmt.Errorf("update interrupted: %w", interruptErr)
		}
	}

	if aborted {
		_ = opts.Log.Emit("error", "run_end", "workspace update aborted (environmental/resource failure)",
			map[string]any{"status": "aborted", "failed": len(failed), "aborted_project": abortedName, "failed_step": abortStep})
		return fmt.Errorf("update aborted at %s (%s): environmental/resource failure — free resources and re-run; remaining repos were not attempted", abortedName, abortStep)
	}
	if len(failed) > 0 {
		_ = opts.Log.Emit("error", "run_end", "workspace update finished with failures",
			map[string]any{"status": "failed", "failed": len(failed), "failed_projects": failed})
		return fmt.Errorf("update failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	_ = opts.Log.Emit("info", "run_end", "workspace update finished", map[string]any{"status": "ok", "failed": 0})
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// elapsedSecs formats the time since start as seconds with one decimal ("1.2s").
func elapsedSecs(start time.Time) string {
	return fmt.Sprintf("%.1fs", time.Since(start).Seconds())
}

// isDirty reports whether repoDir has tracked uncommitted changes — modified or
// staged (untracked files are intentionally allowed; the update flow pulls with
// --autostash). Probes are ordered so a dirty modified tree short-circuits
// before the staged check.
//
// It distinguishes the "changes exist" signal from a genuine probe failure
// (bead pg2-6qtr8): `git diff --quiet` exits 1 iff a diff exists, so exit 1
// means dirty; any OTHER non-zero exit (e.g. 128 — not a repo / bad path) or a
// runner error means cleanliness could not be determined and is returned as a
// non-nil error rather than being silently reported as dirty. Callers decide
// whether an indeterminate probe is fatal, skippable, or a defer.
func (ws *Workspace) isDirty(ctx context.Context, repoDir string) (bool, error) {
	for _, args := range [][]string{
		{"-C", repoDir, "diff", "--quiet"},
		{"-C", repoDir, "diff", "--cached", "--quiet"},
	} {
		if _, err := ws.runner.Run(ctx, "git", args, exec.RunOptions{}); err != nil {
			var cmdErr *exec.CommandError
			if errors.As(err, &cmdErr) && cmdErr.Result.ExitCode == 1 {
				return true, nil
			}
			return false, fmt.Errorf("git diff probe in %s: %w", repoDir, err)
		}
	}
	return false, nil
}

// captureHead returns the trimmed SHA of HEAD for repoDir.
func captureHead(ctx context.Context, runner exec.Runner, repoDir string) (string, error) {
	res, err := runner.Run(ctx, "git",
		[]string{"-C", repoDir, "rev-parse", "HEAD"},
		exec.RunOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}
