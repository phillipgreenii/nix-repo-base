package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ulResultPrefix is the machine-readable line update-locks.sh prints from
// ul_finalize on every exit path: "UL_RESULT transient=<N>". It is pn's only
// window into a green update-locks run — the exit code is 0 whether or not
// steps were classified transient, so an automated update that checks only the
// exit code never sees a permanently-skipped ("silently transient") update
// (ADR 0020). The token is a stable key=value contract, extensible with more
// fields later; only `transient` is consumed today.
const ulResultPrefix = "UL_RESULT "

// parseULTransient extracts the transient-step count from update-locks.sh's
// captured stdout (see ulResultPrefix / ul_finalize). The LAST UL_RESULT line
// wins, since a wrapped step could itself echo the token earlier in the log.
// Returns 0 when no parseable line is present — an older update-locks.sh, a
// skipped/absent script, or a resource-abort that exits before ul_finalize.
func parseULTransient(stdout []byte) int {
	transient := 0
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ulResultPrefix) {
			continue
		}
		for _, field := range strings.Fields(line[len(ulResultPrefix):]) {
			if v, ok := strings.CutPrefix(field, "transient="); ok {
				if n, err := strconv.Atoi(v); err == nil {
					transient = n
				}
			}
		}
	}
	return transient
}

// ulLibResolverRef is the flake app that prints the update-locks lib dir. It
// mirrors the one-liner each update-locks.sh uses; pn resolves it once per run
// and injects the result so the per-repo scripts skip the (remote) evaluation.
const ulLibResolverRef = "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir"

// siblingsOnlySkipBanner returns the --siblings-only "skipping update-locks.sh"
// line for repo name, worded accurately for whether propagateWorkspaceEdges
// actually relocked anything (pg2-vgw3). Both call sites (updateInPlace and
// updateRepoViaWorktree) use this so the wording stays consistent. In either
// case nixpkgs/third-party inputs are left untouched (update-locks.sh is
// skipped); only the workspace-input clause differs:
//   - relocked: propagation moved a sibling rev and committed the bump.
//   - !relocked: no workspace edges, or no rev change — a no-op, so nothing was
//     relocked (the old banner claimed a relock here unconditionally).
func siblingsOnlySkipBanner(name string, relocked bool) string {
	inputs := "no workspace inputs to relock"
	if relocked {
		inputs = "workspace inputs relocked"
	}
	return fmt.Sprintf("  ⊘ %s: --siblings-only — skipping update-locks.sh (%s, nixpkgs/third-party untouched)\n", name, inputs)
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
	// SiblingsOnly relocks ONLY the phillipgreenii-* workspace-sibling flake
	// inputs (the propagateWorkspaceEdges pass) and SKIPS each repo's
	// update-locks.sh, so nixpkgs and other third-party inputs are left
	// untouched. Everything else — topological order, worktree isolation vs
	// InPlace, pull/rebase, push (so a consumer picks up a sibling's
	// freshly-pushed tip) — is unchanged. It is orthogonal to InPlace (composes
	// with either flow). Upgrade never sets it; Recreate (an Upgrade-only
	// marker) and SiblingsOnly are not combined.
	SiblingsOnly bool
	// ULLibDir, when set, is exported as UL_LIB_DIR to each update-locks.sh so
	// it skips its own determine-ul-lib-dir resolution. Resolve it once per run
	// via ResolveULLibDir. Empty leaves each script to resolve for itself.
	ULLibDir string
	// Log, when non-nil, receives a structured JSONL event stream for the run
	// (run_start / project_result / run_end). Nil disables event logging.
	Log *eventlog.Writer
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
	if opts.InPlace {
		return ws.updateInPlace(ctx, out, opts)
	}
	return ws.updateViaWorktree(ctx, out, opts)
}

// updateInPlace pulls each workspace repo, runs its ./update-locks.sh, and pushes.
// Repos without an upstream skip pull/push but still attempt update-locks.
// Repos with a dirty working tree are skipped (non-fatal).
//
// Per-repo failures are aggregated rather than aborting the whole sweep on the
// first error: every repo is attempted and the failing repos are named in the
// returned error at the end (like FlakeCheck). Within a single repo, a failed
// pull marks it failed and skips update-locks and push (the working tree is
// suspect); a failed update-locks still lets push run, since pull succeeded.
//
// The provided context is checked between repos and between sub-steps; a
// cancelled context aborts cleanly with the next ctx.Err() observed.
//
// Repos are processed in topological order (dependencies before consumers) so
// that downstream repos re-lock against already-updated upstreams.
// updateInPlace is a required-terminal command: it errors when no terminal is configured.
func (ws *Workspace) updateInPlace(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if _, err := ws.requireTerminal(ctx, opts.Terminal); err != nil {
		return err
	}
	names := ws.topoAlpha(ctx)
	// Derive the lock once for workspace-edge propagation; effectiveLock (not the
	// possibly-empty ws.lock) is the source topoAlpha trusts (C3).
	edgeLock, _, _ := ws.effectiveLock(ctx)
	_ = opts.Log.Emit("info", "run_start", "workspace update started", map[string]any{
		"terminal": opts.Terminal,
		"projects": len(names),
	})

	var failed []string
	var aborted bool
	var abortedName string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		repoDir := filepath.Join(ws.root, name)

		// Skip (non-fatal) if the working tree is dirty (modified or staged), or
		// if cleanliness could not be determined — either way updating is unsafe.
		dirty, dirtyErr := ws.isDirty(ctx, repoDir)
		if dirtyErr != nil {
			fmt.Fprintf(out, "  ⊘ skipping %s — could not check working tree: %v\n", name, dirtyErr)
			_ = opts.Log.Emit("warn", "project_result", "project skipped (dirty-check failed)",
				map[string]any{"name": name, "outcome": "skipped", "error": dirtyErr.Error()})
			continue
		}
		if dirty {
			fmt.Fprintf(out, "  ⊘ skipping %s — working tree has uncommitted changes\n", name)
			_ = opts.Log.Emit("warn", "project_result", "project skipped (dirty working tree)",
				map[string]any{"name": name, "outcome": "skipped"})
			continue
		}

		fmt.Fprintf(out, "  --== update %s ==--  \n", name)
		hasUp := ws.hasUpstream(ctx, repoDir)
		pullFailed := false
		propagateFailed := false
		relocked := false
		projectFailed := false
		transient := 0

		if hasUp {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				pullFailed = true
				projectFailed = true
			}
		}
		// Propagate workspace-edge inputs (ungated) before update-locks. Skip if
		// pull failed: the working tree is suspect.
		if !pullFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			did, err := ws.propagateWorkspaceEdges(ctx, out, name, repoDir, ws.resolveFlakePath(name), workspaceAliasesFromLock(edgeLock, name))
			if err != nil {
				fmt.Fprintf(out, "  ✗ %s: propagate-edges failed: %v\n", name, err)
				propagateFailed = true
				projectFailed = true
			}
			relocked = did
		}
		// Run update-locks (when present) only if pull and propagation succeeded:
		// a propagation error may have left a dirty tree. A repo without
		// ./update-locks.sh is skipped (not failed) — propagation already
		// maintained its workspace locks.
		if !pullFailed && !propagateFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			switch {
			case opts.SiblingsOnly:
				fmt.Fprint(out, siblingsOnlySkipBanner(name, relocked))
			case fileExists(filepath.Join(repoDir, "update-locks.sh")):
				res, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir, Env: ws.ulSubprocessEnv(opts.ULLibDir), Stdout: out, Stderr: out})
				// res.Stdout is populated on both success and CommandError, so the
				// transient count crosses the boundary even for a hard-failed repo
				// (ul_finalize prints UL_RESULT before its non-zero exit).
				transient = parseULTransient(res.Stdout)
				if err != nil {
					var cmdErr *exec.CommandError
					if errors.As(err, &cmdErr) && cmdErr.Result.ExitCode == ulExitAbort {
						aborted, abortedName = true, name
						fmt.Fprintf(out, "  ⛔ %s: environmental/resource failure — aborting run\n", name)
					} else {
						projectFailed = true
						// Keep going to push whatever update-locks committed.
					}
				}
			default:
				fmt.Fprintf(out, "  ⊘ %s: no update-locks.sh — skipping (workspace inputs already propagated)\n", name)
			}
		}
		// An environmental/resource abort applies to every remaining repo: record
		// it and stop before the push block and the rest of the loop.
		if aborted {
			failed = append(failed, name)
			_ = opts.Log.Emit("error", "project_result", "project aborted", map[string]any{
				"name": name, "outcome": "aborted", "failed_step": "update-locks", "transient": transient,
			})
			break
		}
		// Push only when pull and propagation succeeded (even on partial
		// update-locks failure).
		if hasUp && !pullFailed && !propagateFailed {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("update interrupted: %w", err)
			}
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				projectFailed = true
			}
		}

		if projectFailed {
			failed = append(failed, name)
			step := "push"
			if pullFailed {
				step = "pull"
			} else if propagateFailed {
				step = "propagate-edges"
			}
			_ = opts.Log.Emit("error", "project_result", "project failed", map[string]any{
				"name": name, "outcome": "failed", "failed_step": step, "transient": transient,
			})
		} else {
			// A green repo with transient steps is escalated to warn: update-locks
			// exited 0, but a permanently-transient step keeps silently skipping an
			// update the exit code alone would never surface (ADR 0020).
			level, msg := "info", "project updated"
			if transient > 0 {
				level = "warn"
				msg = fmt.Sprintf("project updated, but %d transient step(s) were skipped this run — a permanently-transient step keeps the run green while an update is silently skipped (ADR 0020)", transient)
			}
			_ = opts.Log.Emit(level, "project_result", msg, map[string]any{
				"name": name, "outcome": "ok", "transient": transient,
			})
		}
	}

	if aborted {
		_ = opts.Log.Emit("error", "run_end", "workspace update aborted (environmental/resource failure)",
			map[string]any{"status": "aborted", "failed": len(failed), "aborted_project": abortedName, "failed_step": "update-locks"})
		return fmt.Errorf("update aborted at %s (update-locks): environmental/resource failure — free resources and re-run; remaining repos were not attempted", abortedName)
	}
	if len(failed) > 0 {
		_ = opts.Log.Emit("error", "run_end", "workspace update finished with failures",
			map[string]any{"status": "failed", "failed": len(failed), "failed_projects": failed})
		return fmt.Errorf("update failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	_ = opts.Log.Emit("info", "run_end", "workspace update finished", map[string]any{"status": "ok", "failed": 0})
	return nil
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
