package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// primaryState classifies a primary checkout for smart integration (step 6).
type primaryState int

const (
	primaryOnCleanMain   primaryState = iota // on main, clean → merge --ff-only
	primaryOnOtherBranch                     // main not checked out → ff the ref
	primaryOnDirtyMain                       // on main but dirty → defer
)

// updateWorktreesSubdir is the dot-prefixed dir under WorkforestsDir() holding the
// ephemeral per-repo update worktrees. Dot-prefixed so WorkforestList and the
// filesystem scanners skip it.
const updateWorktreesSubdir = ".pn-update"

// updateRunStampFn produces the per-run suffix used for the shared branch name
// and per-repo worktree dir names. Timestamp (sub-second) + PID to avoid
// collisions between runs. A package var so tests can pin it deterministically.
var updateRunStampFn = func() string {
	return fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102-150405.000"), os.Getpid())
}

// inWorkforest reports whether ws.root is a coordinated workforest set created
// by `pn workspace workforest add` — i.e. it lives under the configured
// workforests dir (<workforests_dir>/<branch>). The worktree-isolation update flow is
// invalid inside a set: the set's repos are worktrees on a shared feature branch
// with `main` checked out in the canonical clones, so a nested worktree-add +
// ff-main would violate the set's P1 invariant. It is also consulted by Push,
// which skips its canonical-clone sibling relock inside a set. Detection is
// structural and tolerant of a slashed branch that nests the set dir more than
// one segment below the workforests dir — see Workspace.workforestLocation.
func (ws *Workspace) inWorkforest() bool {
	_, in := ws.workforestLocation()
	return in
}

// primaryMainState probes the primary checkout's branch + cleanliness to decide
// how step 6 advances main. A non-"main" current branch (or a probe error) is
// treated as primaryOnOtherBranch: main is not checked out, so its ref can be
// fast-forwarded without touching the working tree.
func (ws *Workspace) primaryMainState(ctx context.Context, primary string) primaryState {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", primary, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	cur := ""
	if err == nil {
		cur = strings.TrimSpace(string(res.Stdout))
	}
	// A detached HEAD (rev-parse --abbrev-ref HEAD prints "HEAD") and a probe
	// error both intentionally fall into primaryOnOtherBranch: step 6 then
	// advances main via `fetch . branch:main`, a ref-only ff that never touches
	// the non-main working tree.
	if cur != "main" {
		return primaryOnOtherBranch
	}
	// A probe error means cleanliness is indeterminate; defer rather than risk a
	// merge --ff-only that assumes a clean primary (bead pg2-6qtr8).
	if dirty, err := ws.isDirty(ctx, primary); err != nil || dirty {
		return primaryOnDirtyMain
	}
	return primaryOnCleanMain
}

// repoStatus is the outcome classification for a per-repo worktree update. It is
// a string alias so eventlog "outcome" fields and the != statusOK comparisons
// keep working without conversions.
type repoStatus = string

const (
	statusOK       repoStatus = "ok"
	statusFailed   repoStatus = "failed"
	statusDeferred repoStatus = "deferred"
	// statusAborted: update-locks.sh signalled an environmental / resource
	// exhaustion failure (see ulExitAbort). Unlike statusFailed, it stops the
	// whole run — every remaining repo would fail identically.
	statusAborted repoStatus = "aborted"
)

// ulExitAbort is the exit code update-locks.sh uses to signal an environmental /
// resource-exhaustion abort (out of disk, or an unhealthy nix daemon). pn stops
// the run rather than marching into the same wall. Contract: the UL_RC_ABORT
// sentinel in lib/scripts/update-locks-lib.bash (ADR 0020).
const ulExitAbort = 77

// repoOutcome records one repo's worktree-update result for the run summary.
type repoOutcome struct {
	name       string
	status     repoStatus
	failedStep string
	worktree   string // left-behind worktree path when status != ok
	branch     string // left-behind branch when status != ok
	note       string // recovery hint / human note
	transient  int    // steps update-locks.sh classified transient (ADR 0020); >0 warns
	// relockFailed: the full-scope all-inputs relock failed after update-locks.sh
	// succeeded; the repo is still integrated (status ok) and warns.
	relockFailed bool
	// interruptErr is set when the relock returned a context error: the repo
	// ends and so does the run, even if the outer context is still live.
	interruptErr error
}

// updateViaWorktree runs the worktree-isolated update over all repos in
// topological order. See ADR 0009 and the design spec for the per-repo
// algorithm; this is the outer loop (terminal guard, UL_LIB_DIR resolve,
// eventlog, summary) and updateRepoViaWorktree is the body.
func (ws *Workspace) updateViaWorktree(ctx context.Context, out io.Writer, opts UpdateOptions) error {
	if _, err := ws.requireTerminal(ctx, opts.Terminal); err != nil {
		return err
	}
	if ws.inWorkforest() {
		return fmt.Errorf("update: refusing the worktree-isolation flow inside a coordinated workforest set (%s); run `pn workspace update --in-place` to relock the set in place", ws.root)
	}
	// Resolve UL_LIB_DIR once: explicit option → pre-set env (lets CI/smoke inject
	// without nix) → nix resolver. Each consumer update-locks.sh clobbers
	// WORKSPACE_ROOT to SCRIPT_DIR/.., so a non-empty UL_LIB_DIR is the only safe
	// relock path in a worktree (ADR 0009 B1); empty is fatal.
	//
	// Only the full scope runs update-locks.sh, so only it consumes UL_LIB_DIR;
	// resolving (and hard-failing on) it for siblings or flake would needlessly
	// require the nix resolver, breaking the headless doctor-fix path.
	scope := opts.scope()
	ulLibDir := opts.ULLibDir
	if scope == ScopeFull {
		if ulLibDir == "" {
			ulLibDir = os.Getenv("UL_LIB_DIR")
		}
		if ulLibDir == "" {
			ulLibDir = ws.ResolveULLibDir(ctx)
		}
		if ulLibDir == "" {
			return fmt.Errorf("update: could not resolve UL_LIB_DIR (set UL_LIB_DIR or fix determine-ul-lib-dir); refusing to relock in a worktree without it (use --in-place to update on main)")
		}
	}

	runTS := updateRunStampFn()
	branch := "pn-update/" + runTS
	names := ws.topoAlpha(ctx)
	// The sibling-alias set is needed ONLY by the siblings scope, which relocks
	// just those inputs. The other scopes relock every input through nix, sibling
	// or not, from its declared remote (ADR 0023 item 1), so deriving the lock for
	// them would buy a nix eval per repo and use nothing. When it IS derived, use
	// effectiveLock (the same source topoAlpha trusts) rather than ws.lock, which
	// is empty on a fresh/stale checkout and would silently skip every repo (C3).
	var edgeLock *Lock
	if scope == ScopeSiblings {
		edgeLock, _, _ = ws.effectiveLock(ctx)
	}

	_ = opts.Log.Emit("info", "run_start", "workspace update (worktree) started", map[string]any{
		"terminal": opts.Terminal, "projects": len(names), "branch": branch,
		"scope": string(scope), "root": ws.root,
	})

	outcomes := make([]repoOutcome, 0, len(names))
	first := true
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		// Blank line between repo blocks (not before the first).
		if !first {
			fmt.Fprintln(out)
		}
		first = false
		repoCtx, endRepo := telemetry.StartRepo(ctx, name)
		oc := ws.updateRepoViaWorktree(repoCtx, out, name, branch, runTS, ulLibDir, workspaceAliasesFromLock(edgeLock, name), scope)
		if oc.status != statusOK {
			endRepo(fmt.Errorf("update %s: %s", name, oc.status))
		} else {
			endRepo(nil)
		}
		level, outcome := "info", statusOK
		msg := "project " + oc.status
		switch {
		case oc.status != statusOK:
			level, outcome = "error", oc.status
		case oc.transient > 0 || oc.relockFailed:
			// A green repo (integrated ok) with transient steps, or whose full-scope
			// relock failed after update-locks.sh succeeded, escalates to warn: the
			// exit code alone would never surface a silently skipped update (ADR 0020).
			level = "warn"
			msg = "project ok"
			if oc.transient > 0 {
				msg = fmt.Sprintf("project ok, but %d transient step(s) were skipped this run — a permanently-transient step keeps the run green while an update is silently skipped (ADR 0020)", oc.transient)
			}
			if oc.relockFailed {
				msg += "; the all-inputs relock failed after one retry, so flake inputs were not moved"
			}
		}
		fields := map[string]any{
			"name": oc.name, "outcome": outcome, "failed_step": oc.failedStep, "note": oc.note, "transient": oc.transient,
		}
		if oc.relockFailed {
			fields["relock_failed"] = true
		}
		_ = opts.Log.Emit(level, "project_result", msg, fields)
		outcomes = append(outcomes, oc)
		// A cancel during the repo ends the run here, whatever step it hit.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		if oc.interruptErr != nil {
			return fmt.Errorf("update interrupted: %w", oc.interruptErr)
		}
		// An environmental/resource abort applies to every remaining repo, so stop
		// the run here rather than attempting them and failing identically.
		if oc.status == statusAborted {
			break
		}
	}

	printUpdateSummary(out, outcomes)

	var failed []string
	var aborted *repoOutcome
	for i := range outcomes {
		if outcomes[i].status == statusAborted {
			aborted = &outcomes[i]
		}
		if outcomes[i].status != statusOK {
			failed = append(failed, outcomes[i].name)
		}
	}
	if aborted != nil {
		_ = opts.Log.Emit("error", "run_end", "workspace update aborted (environmental/resource failure)",
			map[string]any{"status": "aborted", "failed": len(failed), "aborted_project": aborted.name, "failed_step": aborted.failedStep})
		return fmt.Errorf("update aborted at %s (%s): environmental/resource failure — free resources and re-run; remaining repos were not attempted", aborted.name, aborted.failedStep)
	}
	if len(failed) > 0 {
		_ = opts.Log.Emit("error", "run_end", "workspace update finished with failures",
			map[string]any{"status": "failed", "failed": len(failed), "failed_projects": failed})
		return fmt.Errorf("update failed/deferred in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	_ = opts.Log.Emit("info", "run_end", "workspace update finished", map[string]any{"status": "ok", "failed": 0})
	return nil
}

// updateRepoViaWorktree runs the per-repo worktree flow (worktree-add → sync →
// relock → rebase → integrate). It is LOCAL-ONLY: it fetches, but it never
// pushes and never propagates workspace-sibling locks — both moved to
// `pn workspace push` (ADR 0023). It never returns an error either: every
// failure is captured in the returned repoOutcome and the worktree + branch are
// left in place (leave-on-failure). Only a fully successful integration removes
// them.
func (ws *Workspace) updateRepoViaWorktree(ctx context.Context, out io.Writer, name, branch, runTS, ulLibDir string, aliases []string, scope UpdateScope) repoOutcome {
	primary := filepath.Join(ws.root, name)
	wt := filepath.Join(ws.WorkforestsDir(), updateWorktreesSubdir, name+"-"+runTS)
	oc := repoOutcome{name: name, worktree: wt, branch: branch}

	fmt.Fprintf(out, "  --== update %s (worktree) ==--  \n", name)

	git := func(args ...string) error {
		_, err := ws.runner.Run(ctx, "git", append([]string{"-C"}, args...), exec.RunOptions{Stdout: out, Stderr: out})
		return err
	}
	fail := func(step string, cause error, hint string) repoOutcome {
		oc.status, oc.failedStep = statusFailed, step
		switch {
		case cause != nil && hint != "":
			oc.note = hint + ": " + cause.Error()
		case cause != nil:
			oc.note = cause.Error()
		default:
			oc.note = hint
		}
		fmt.Fprintf(out, "  ✗ %s: failed at %s — worktree left at %s (branch %s)\n", name, step, wt, branch)
		return oc
	}
	// abort marks an environmental / resource-exhaustion failure (update-locks.sh
	// exit ulExitAbort). Like fail it leaves the worktree/branch for inspection,
	// but the outer loop stops the whole run on this status.
	abort := func(step string, cause error) repoOutcome {
		oc.status, oc.failedStep = statusAborted, step
		if cause != nil {
			oc.note = cause.Error()
		}
		fmt.Fprintf(out, "  ⛔ %s: environmental/resource failure at %s — aborting run (worktree left at %s, branch %s)\n", name, step, wt, branch)
		return oc
	}

	// Step 1: create worktree + branch off local main.
	if err := git(primary, "worktree", "add", "-b", branch, wt, "main"); err != nil {
		oc.status, oc.failedStep, oc.worktree, oc.branch = statusFailed, "worktree-add", "", ""
		oc.note = err.Error()
		fmt.Fprintf(out, "  ✗ %s: worktree add failed (stale leftover? run `pn workspace workforest prune`): %v\n", name, err)
		return oc
	}

	// No hook config is linked into the new worktree: it reaches the clone's
	// shared per-clone hook bundle through the stubs in the common hooks dir,
	// and nothing is ever written into a working tree (ADR 0032).

	// Step 2: sync branch to remote main.
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("fetch-origin", err, "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main", err, "rebase conflict aborted")
	}

	// Step 3: relock per scope (ADR 0045); nothing here pushes (ADR 0023):
	//
	//   - siblings: relock ONLY the workspace-sibling inputs, from their declared
	//     remote URLs, and skip update-locks.sh.
	//   - flake: one `nix flake update --refresh` over every input; skips
	//     update-locks.sh.
	//   - full: ./update-locks.sh, then (unless it failed or aborted) the
	//     all-inputs relock. A repo without the script skips straight to the relock.
	if scope == ScopeSiblings {
		phaseCtx, endRelock := telemetry.StartRelock(ctx, "siblings")
		relocked, err := ws.propagateWorkspaceEdges(phaseCtx, out, name, wt, ws.resolveFlakePath(name), aliases)
		endRelock(boolToInt(relocked), err)
		if err != nil {
			return fail("relock-siblings", err, "")
		}
		fmt.Fprint(out, scopeSkipBanner(name, scope, relocked))
	}
	if scope == ScopeFull {
		if fileExists(filepath.Join(wt, "update-locks.sh")) {
			res, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{
				Dir: wt, Env: ws.ulSubprocessEnv(ulLibDir), Stdout: out, Stderr: out, PropagateTrace: true,
			})
			// res.Stdout is captured on success and on a hard CommandError alike, so the
			// transient count crosses the boundary even when the repo later fails; a
			// resource-abort exits before ul_finalize prints UL_RESULT, leaving it 0.
			oc.transient = parseULTransient(res.Stdout)
			if err != nil {
				var cmdErr *exec.CommandError
				if errors.As(err, &cmdErr) && cmdErr.Result.ExitCode == ulExitAbort {
					return abort("update-locks", err)
				}
				return fail("update-locks", err, "")
			}
		} else {
			fmt.Fprintf(out, "  ⊘ %s: no update-locks.sh — skipping its per-package steps (flake inputs relocked next)\n", name)
		}
	}
	if scope == ScopeFlake || scope == ScopeFull {
		changed, err := ws.relockFlakeInputs(ctx, out, name, wt)
		if err != nil {
			switch classifyRelockErr(ctx, scope, err) {
			case relockWarn:
				oc.relockFailed = true
				fmt.Fprint(out, relockWarning(name, err))
			case relockAbortRun:
				return abort("relock-flake", err)
			case relockInterrupt:
				oc.interruptErr = err
				return fail("relock-flake", err, "")
			default:
				return fail("relock-flake", err, "")
			}
		} else if scope == ScopeFlake {
			fmt.Fprint(out, scopeSkipBanner(name, scope, changed > 0))
		}
	}

	// Step 4: rebase onto local main FIRST (catch unpushed local commits).
	if err := git(wt, "rebase", "main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-local-main", err, "rebase conflict aborted")
	}

	// Step 5: re-fetch + rebase onto origin/main (catch remote drift).
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("refetch-origin", err, "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main-2", err, "rebase conflict aborted")
	}

	// There is NO push step. Update integrates onto the LOCAL primary main and
	// stops; publishing is `pn workspace push`, which pushes from the canonical
	// clone (ADR 0023 items 1 and 4). Removing it also removes ADR 0009's
	// "asymmetric defer state" from update entirely: nothing can advance remote
	// main ahead of local main here, so a deferred integration can only ever leave
	// local main BEHIND its own worktree branch — recoverable with a plain
	// fast-forward, never a reset.
	//
	// Step 6: advance local primary main (smart).
	switch ws.primaryMainState(ctx, primary) {
	case primaryOnCleanMain:
		if err := git(primary, "merge", "--ff-only", branch); err != nil {
			oc.status, oc.failedStep = statusDeferred, "ff-merge"
			// NOTHING WAS PUSHED (ADR 0023), so the recovery target is the relocked
			// BRANCH, not origin/main. The branch was rebased onto local main and then
			// onto origin/main, so it already contains both — resetting main to
			// origin/main instead would discard this run's relock and any unpushed
			// local commits it replayed. This is also why ADR 0009's asymmetric-defer
			// state no longer arises here: remote main cannot be ahead of local main
			// because of anything update did.
			oc.note = fmt.Sprintf("local main is not fast-forwardable to the relocked branch (origin/main advanced mid-run); nothing was pushed — inspect, then advance: git -C %s reset --hard %s", primary, branch)
			fmt.Fprintf(out, "  ⚠ %s: ff-merge deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnOtherBranch:
		if err := git(primary, "fetch", ".", branch+":main"); err != nil {
			oc.status, oc.failedStep = statusDeferred, "ff-ref"
			oc.note = fmt.Sprintf("local main diverged from the relocked branch; nothing was pushed — inspect, then advance: git -C %s branch -f main %s", primary, branch)
			fmt.Fprintf(out, "  ⚠ %s: main ff deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnDirtyMain:
		// ff-first: a dirty file that does NOT collide with the ff'd paths (the
		// common case — update only touches lock files) fast-forwards fine. Only
		// autostash + retry when the direct ff is genuinely blocked. (Chosen over
		// always-autostash, which risks silently leaving main mid-merge.)
		if err := git(primary, "merge", "--ff-only", branch); err == nil {
			break // success → fall through to step 7 cleanup, status stays OK
		}
		// ff blocked by the dirty tree. Autostash the tracked changes and retry.
		// Bare `stash push` is tracked-only by default (untracked stay put).
		fmt.Fprintf(out, "  ↻ %s: primary main dirty — autostashing to fast-forward\n", name)
		if err := git(primary, "stash", "push", "-m", "pn-update autostash "+branch); err != nil {
			oc.status, oc.failedStep = statusDeferred, "integrate"
			oc.note = "primary on dirty main; autostash failed — commit/stash then ff main from the branch"
			fmt.Fprintf(out, "  ⚠ %s: integration deferred — autostash failed; worktree at %s (branch %s)\n", name, wt, branch)
			return oc
		}
		// Retry the ff against the now-clean tree.
		if err := git(primary, "merge", "--ff-only", branch); err != nil {
			// Not fast-forwardable (remote advanced/diverged), not a dirty-file
			// issue. Restore the user's tree before deferring.
			oc.status, oc.failedStep = statusDeferred, "ff-merge"
			if perr := git(primary, "stash", "pop"); perr != nil {
				// The restore pop failed too: the autostashed changes are stranded in
				// the stash. Don't claim they were restored — point at the stash so the
				// user can recover them (mirrors the hard-defer autostash-pop note below).
				oc.note = fmt.Sprintf("local main is not fast-forwardable to the relocked branch and restoring your changes failed; nothing was pushed — advance main then recover your stash: git -C %s reset --hard %s; your changes are in `git stash list`", primary, branch)
				fmt.Fprintf(out, "  ⚠ %s: ff-merge deferred (stash retained) — %s (worktree at %s)\n", name, oc.note, wt)
				return oc
			}
			oc.note = fmt.Sprintf("local main is not fast-forwardable to the relocked branch (origin/main advanced mid-run); nothing was pushed — inspect, then advance: git -C %s reset --hard %s", primary, branch)
			fmt.Fprintf(out, "  ⚠ %s: ff-merge deferred (stash restored) — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
		// ff landed; restore the stash.
		if err := git(primary, "stash", "pop"); err != nil {
			// HARD DEFER: integration landed but primary main is now mid-merge with
			// conflict markers + a retained stash. Do NOT report OK, do NOT clean up.
			oc.status, oc.failedStep = statusDeferred, "autostash-pop"
			oc.note = fmt.Sprintf("integrated, but autostash pop conflicted on primary main — resolve conflicts in %s then drop the stash (`git -C %s stash drop`); your changes are in `git stash list`", primary, primary)
			fmt.Fprintf(out, "  ⚠ %s: integrated but autostash pop conflicted — %s\n", name, oc.note)
			return oc
		}
		// success → fall through to step 7 cleanup
	}

	// Step 7: success — stop the worktree's fsmonitor daemon (best-effort; it is
	// keyed by worktree path and is NOT torn down by `worktree remove`, so it
	// would orphan and linger), then remove the worktree, then the branch.
	ws.stopFsmonitorDaemon(ctx, wt)
	if err := git(primary, "worktree", "remove", wt); err != nil {
		oc.status, oc.note = statusOK, "integrated, but worktree remove failed — run `pn workspace workforest prune`"
		fmt.Fprintf(out, "  ⚠ %s: integrated, but worktree remove failed\n", name)
		return oc
	}
	// Force-delete (-D, not -d): integration already advanced local main to this
	// branch, so the ephemeral branch is disposable. A repo whose
	// worktree branch is not a strict ancestor of main (e.g. a no-op skip where the
	// branch tip never merged) makes `-d` refuse with "not fully merged", leaking a
	// pn-update/<ts> branch every run (tc-1zbpk W2). -D is always safe here.
	_ = git(primary, "branch", "-D", branch)
	oc.status, oc.worktree, oc.branch = statusOK, "", ""
	fmt.Fprintf(out, "  ✓ %s: updated and integrated\n", name)
	return oc
}

// printUpdateSummary prints one line per repo: outcome and, for non-ok repos,
// the worktree/branch left behind and the recovery note. An "ok" outcome with a
// note (e.g. worktree-remove failure left residue on disk) surfaces its hint too.
func printUpdateSummary(out io.Writer, outcomes []repoOutcome) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== Update Summary ===")
	for _, oc := range outcomes {
		switch oc.status {
		case statusOK:
			if oc.relockFailed {
				fmt.Fprintf(out, "  ⚠ %s — ok, relock failed; re-run 'pn workspace update --scope flake' to move flake inputs\n", oc.name)
			} else {
				fmt.Fprintf(out, "  ✓ %s — ok\n", oc.name)
			}
			if oc.note != "" {
				fmt.Fprintf(out, "      ↳ %s\n", oc.note)
			}
		default:
			fmt.Fprintf(out, "  ✗ %s — %s@%s; worktree %s (branch %s)\n", oc.name, oc.status, oc.failedStep, oc.worktree, oc.branch)
			if oc.note != "" {
				fmt.Fprintf(out, "      ↳ %s\n", oc.note)
			}
		}
	}
}
