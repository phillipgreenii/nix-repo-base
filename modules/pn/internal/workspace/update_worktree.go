package workspace

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// primaryState classifies a primary checkout for smart integration (step 7).
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
// by `pn workspace workforest add` — i.e. it lives directly under the configured
// workforests dir (<workforests_dir>/<branch>). The worktree-isolation update flow is
// invalid inside a set: the set's repos are worktrees on a shared feature branch
// with `main` checked out in the canonical clones, so a nested worktree-add +
// push-to-main + ff-main would violate the set's P1 invariant. Detection is
// structural — a set always lives directly under the workforests dir.
func (ws *Workspace) inWorkforest() bool {
	return filepath.Base(filepath.Dir(ws.root)) == filepath.Base(ws.config.WorkforestsDirName())
}

// primaryMainState probes the primary checkout's branch + cleanliness to decide
// how step 7 advances main. A non-"main" current branch (or a probe error) is
// treated as primaryOnOtherBranch: main is not checked out, so its ref can be
// fast-forwarded without touching the working tree.
func (ws *Workspace) primaryMainState(ctx context.Context, primary string) primaryState {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", primary, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	cur := ""
	if err == nil {
		cur = strings.TrimSpace(string(res.Stdout))
	}
	// A detached HEAD (rev-parse --abbrev-ref HEAD prints "HEAD") and a probe
	// error both intentionally fall into primaryOnOtherBranch: step 7 then
	// advances main via `fetch . branch:main`, a ref-only ff that never touches
	// the non-main working tree.
	if cur != "main" {
		return primaryOnOtherBranch
	}
	if ws.isDirty(ctx, primary) {
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
)

// repoOutcome records one repo's worktree-update result for the run summary.
type repoOutcome struct {
	name       string
	status     repoStatus
	failedStep string
	worktree   string // left-behind worktree path when status != ok
	branch     string // left-behind branch when status != ok
	rev        string // rev to record in revs.json (ok or pushed-but-deferred)
	note       string // recovery hint / human note
}

// updateViaWorktree runs the worktree-isolated update over all repos in
// topological order. See ADR 0009 and the design spec for the per-repo
// algorithm; this is the outer loop (terminal guard, UL_LIB_DIR resolve,
// rev-lock rewrite, eventlog, summary) and updateRepoViaWorktree is the body.
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
	// SiblingsOnly skips update-locks.sh entirely, so UL_LIB_DIR is never
	// consumed — resolving (and hard-failing on) it would needlessly require the
	// nix resolver, breaking the headless doctor-fix path. Skip the block.
	ulLibDir := opts.ULLibDir
	if !opts.SiblingsOnly {
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
	// Derive the lock once for workspace-edge propagation. Use effectiveLock (the
	// same source topoAlpha trusts) rather than ws.lock, which is empty on a
	// fresh/stale checkout and would silently skip every repo (C3).
	edgeLock, _, _ := ws.effectiveLock(ctx)

	_ = opts.Log.Emit("info", "run_start", "workspace update (worktree) started", map[string]any{
		"terminal": opts.Terminal, "projects": len(names), "branch": branch,
	})

	// Seed from the existing rev-lock so untouched repos keep their entries.
	revs := make(map[string]LockedRepo, len(names))
	if ws.revLock != nil {
		maps.Copy(revs, ws.revLock.Repos)
	}

	outcomes := make([]repoOutcome, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("update interrupted: %w", err)
		}
		oc := ws.updateRepoViaWorktree(ctx, out, name, branch, runTS, ulLibDir, workspaceAliasesFromLock(edgeLock, name), opts.SiblingsOnly)
		if oc.rev != "" {
			revs[name] = LockedRepo{URL: displayURL(ws.config.Repos[name]), Rev: oc.rev}
		}
		level, outcome := "info", statusOK
		if oc.status != statusOK {
			level, outcome = "error", oc.status
		}
		_ = opts.Log.Emit(level, "project_result", "project "+oc.status, map[string]any{
			"name": oc.name, "outcome": outcome, "failed_step": oc.failedStep, "note": oc.note,
		})
		outcomes = append(outcomes, oc)
	}

	if err := WriteRevLock(filepath.Join(ws.root, RevLockFileName), &RevLock{Repos: revs}); err != nil {
		return fmt.Errorf("write rev lock: %w", err)
	}

	printUpdateSummary(out, outcomes)

	var failed []string
	for _, oc := range outcomes {
		if oc.status != statusOK {
			failed = append(failed, oc.name)
		}
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
// propagate workspace edges → update-locks → rebase → push → integrate). It never returns an
// error: every failure is captured in the returned repoOutcome and the worktree
// + branch are left in place (leave-on-failure). Only a fully successful
// integration removes them.
func (ws *Workspace) updateRepoViaWorktree(ctx context.Context, out io.Writer, name, branch, runTS, ulLibDir string, aliases []string, siblingsOnly bool) repoOutcome {
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

	// Step 1: create worktree + branch off local main.
	if err := git(primary, "worktree", "add", "-b", branch, wt, "main"); err != nil {
		oc.status, oc.failedStep, oc.worktree, oc.branch = statusFailed, "worktree-add", "", ""
		oc.note = err.Error()
		fmt.Fprintf(out, "  ✗ %s: worktree add failed (stale leftover? run `pn workspace workforest prune`): %v\n", name, err)
		return oc
	}

	// Step 2: sync branch to remote main.
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("fetch-origin", err, "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main", err, "rebase conflict aborted")
	}

	// Step 3: propagate workspace-edge inputs (ungated) — relock this repo's
	// workspace-sibling flake inputs to their upstreams' just-integrated revs.
	relocked, err := ws.propagateWorkspaceEdges(ctx, out, name, wt, ws.resolveFlakePath(name), aliases)
	if err != nil {
		return fail("propagate-edges", err, "")
	}

	// Step 4: run the existing update-locks in the worktree, when present. A repo
	// without ./update-locks.sh is skipped (not failed): the propagation pass
	// above already maintains its workspace locks. --siblings-only skips it
	// unconditionally so nixpkgs/third-party inputs are left untouched.
	switch {
	case siblingsOnly:
		fmt.Fprint(out, siblingsOnlySkipBanner(name, relocked))
	case fileExists(filepath.Join(wt, "update-locks.sh")):
		if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{
			Dir: wt, Env: ws.ulSubprocessEnv(ulLibDir), Stdout: out, Stderr: out,
		}); err != nil {
			return fail("update-locks", err, "")
		}
	default:
		fmt.Fprintf(out, "  ⊘ %s: no update-locks.sh — skipping (workspace inputs already propagated)\n", name)
	}

	// Step 5: rebase onto local main FIRST (catch unpushed local commits).
	if err := git(wt, "rebase", "main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-local-main", err, "rebase conflict aborted")
	}

	// Step 6: re-fetch + rebase onto origin/main (catch remote drift).
	if err := git(wt, "fetch", "origin"); err != nil {
		return fail("refetch-origin", err, "")
	}
	if err := git(wt, "rebase", "origin/main"); err != nil {
		_ = git(wt, "rebase", "--abort")
		return fail("rebase-origin-main-2", err, "rebase conflict aborted")
	}

	// Capture the integrated tip (the rev downstream consumers relock against).
	rev, err := captureHead(ctx, ws.runner, wt)
	if err != nil {
		return fail("capture-rev", err, "")
	}

	// Step 7: publish — push branch to remote main from the worktree.
	if err := git(wt, "push", "origin", "HEAD:main"); err != nil {
		return fail("push", err, "remote main may have advanced; resolve manually and re-run")
	}
	// Remote main is now at rev. Record it even if step 8 defers, so revs.json
	// matches what downstream repos will relock against (ADR 0009 N1).
	oc.rev = rev

	// Step 8: advance local primary main (smart).
	switch ws.primaryMainState(ctx, primary) {
	case primaryOnCleanMain:
		if err := git(primary, "merge", "--ff-only", branch); err != nil {
			oc.status, oc.failedStep = statusDeferred, "ff-merge"
			oc.note = fmt.Sprintf("remote main advanced; reset local: git -C %s reset --hard origin/main", primary)
			fmt.Fprintf(out, "  ⚠ %s: ff-merge deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnOtherBranch:
		if err := git(primary, "fetch", ".", branch+":main"); err != nil {
			oc.status, oc.failedStep = statusDeferred, "ff-ref"
			oc.note = fmt.Sprintf("local main diverged; reset: git -C %s branch -f main origin/main", primary)
			fmt.Fprintf(out, "  ⚠ %s: main ff deferred — %s (worktree at %s)\n", name, oc.note, wt)
			return oc
		}
	case primaryOnDirtyMain:
		// ff-first: a dirty file that does NOT collide with the ff'd paths (the
		// common case — update only touches lock files) fast-forwards fine. Only
		// autostash + retry when the direct ff is genuinely blocked. (Chosen over
		// always-autostash, which risks silently leaving main mid-merge.)
		if err := git(primary, "merge", "--ff-only", branch); err == nil {
			break // success → fall through to step 9 cleanup, status stays OK
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
			_ = git(primary, "stash", "pop")
			oc.status, oc.failedStep = statusDeferred, "ff-merge"
			oc.note = fmt.Sprintf("remote main advanced; reset local: git -C %s reset --hard origin/main", primary)
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
		// success → fall through to step 9 cleanup
	}

	// Step 9: success — remove worktree, then branch.
	if err := git(primary, "worktree", "remove", wt); err != nil {
		oc.status, oc.note = statusOK, "integrated, but worktree remove failed — run `pn workspace workforest prune`"
		fmt.Fprintf(out, "  ⚠ %s: integrated, but worktree remove failed\n", name)
		return oc
	}
	// Force-delete (-D, not -d): integration already pushed HEAD to remote main and
	// advanced local main, so the ephemeral branch is disposable. A repo whose
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
			fmt.Fprintf(out, "  ✓ %s — ok\n", oc.name)
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
