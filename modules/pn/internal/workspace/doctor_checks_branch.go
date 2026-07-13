// internal/workspace/doctor_checks_branch.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// checkBranches audits branch placement, local-vs-remote sync, and tree
// cleanliness. Worktree mode relaxes: branch-uniform instead of branch-current,
// branch-synced dropped, tree-clean is a warning.
func (ws *Workspace) checkBranches(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding
	present := ws.presentRepoDirs()

	if env.mode == "worktree" {
		fs = append(fs, ws.checkBranchUniform(ctx, present)...)
	}

	for name, dir := range present {
		rc := ws.config.Repos[name]
		branch := rc.Branch
		if branch == "" {
			branch = "main"
		}

		// branch-current (primary only; worktree uniformity handled above)
		if env.mode == "primary" {
			cur, detached, _, ok := ws.branchInfo(ctx, dir)
			if ok && (detached || cur != branch) {
				fs = append(fs, Finding{
					CheckID: "branch-current", Repo: name, Severity: SevError,
					Message: fmt.Sprintf("repo %q is not on its default branch %q (on %q)", name, branch, branchOrDetached(cur, detached)),
					Fixable: true,
					fix: func(c context.Context) error {
						return ws.switchToDefaultBranch(c, dir, branch)
					},
					Manual: fmt.Sprintf("git -C %s switch %s", dir, branch),
				})
			}
		}

		// tree-clean
		dirty, dirtyErr := ws.isDirty(ctx, dir)
		switch {
		case dirtyErr != nil:
			fs = append(fs, Finding{
				CheckID: "tree-clean", Repo: name, Severity: SevError,
				Message: fmt.Sprintf("repo %q: could not determine tree cleanliness: %v", name, dirtyErr),
			})
		case dirty:
			sev := SevError
			if env.mode == "worktree" {
				sev = SevWarning
			}
			fs = append(fs, Finding{
				CheckID: "tree-clean", Repo: name, Severity: sev,
				Message: fmt.Sprintf("repo %q has uncommitted tracked changes (local build would differ from remote)", name),
				Manual:  fmt.Sprintf("commit or stash:  git -C %s stash", dir),
			})
		}

		// branch-synced (primary only)
		if env.mode == "primary" {
			if env.skipped[name] {
				fs = append(fs, Finding{
					CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote comparison skipped",
				})
				continue
			}
			ref := env.refRev[name]
			if ref == "" {
				fs = append(fs, Finding{
					CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote rev unresolved (no upstream?)",
				})
				continue
			}
			local, err := captureHead(ctx, ws.runner, dir)
			if err == nil && local != ref {
				fs = append(fs, ws.branchSyncedFinding(ctx, name, dir, branch, ref))
			}
		}
	}
	return fs
}

// branchSyncedFinding classifies the local-vs-remote divergence and builds the
// finding. ref is the SHA that TRIGGERED the finding (env.refRev[name], the live
// `git ls-remote` head resolved in resolveRefRevs) — the same value the trigger
// compared against local HEAD.
//
// Two-source consistency (see the review of commit d04f29a): classification MUST
// use that same ref SHA, NOT the local remote-tracking ref (<remote>/<branch>).
// The tracking ref comes from a best-effort fetch whose error resolveRefRevs
// discards; a stale/failed fetch would let a repo that ls-remote proves diverged
// be mis-classified ahead-only against a stale tracking ref, so --fix would
// attempt a push. Classifying against ref removes that asymmetry: the object may
// be missing locally (a failed fetch leaves it absent for the behind case), in
// which case merge-base --is-ancestor errors and we conservatively fall through
// to the diverged/manual arm — strictly safer than a mis-fired fast-forward.
//
// remote is the repo's resolved canonical push remote (resolvePushRemote), not a
// hardcoded "origin": the fix commands and manual hints must name the remote the
// repo actually pushes to.
func (ws *Workspace) branchSyncedFinding(ctx context.Context, name, dir, branch, ref string) Finding {
	remote := ws.resolveBranchRemote(ctx, dir, branch)
	local, _ := captureHead(ctx, ws.runner, dir)
	behind := ws.isStrictlyBehind(ctx, dir, ref)
	f := Finding{
		CheckID: "branch-synced", Repo: name, Severity: SevError,
		Message: fmt.Sprintf("repo %q local HEAD %s != remote %s (%s)", name, short(local), short(ref), ws.aheadBehind(ctx, dir)),
	}
	switch {
	case behind:
		// local HEAD is an ancestor of the remote ref → fast-forward pull.
		f.Fixable = true
		f.fix = func(c context.Context) error { return ws.fastForwardIfBehind(c, dir, remote, branch) }
		f.Manual = fmt.Sprintf("git -C %s merge --ff-only %s/%s", dir, remote, branch)
	case ws.isStrictlyAhead(ctx, dir, ref):
		// the remote ref is an ancestor of local HEAD → fast-forward push.
		f.Fixable = true
		f.fix = func(c context.Context) error { return ws.pushBranch(c, dir, remote, branch) }
		f.Manual = fmt.Sprintf("git -C %s push %s %s", dir, remote, branch)
	default:
		// genuinely diverged (ahead AND behind) → manual rebase; ff is unsafe.
		f.Manual = fmt.Sprintf("local diverged from %s/%s — resolve by hand:  git -C %s rebase %s/%s", remote, branch, dir, remote, branch)
	}
	return f
}

// resolveBranchRemote returns the canonical push remote for dir's branch,
// reusing the same convention chain `pn workspace push` honors
// (resolvePushRemote: flag → single-remote → branch.<b>.pushRemote →
// remote.pushDefault → origin → error). On resolution failure it falls back to
// "origin" so the finding still renders a hint (the resolved remote is best-effort
// here — a genuinely missing remote surfaces later as a fix-failed error).
func (ws *Workspace) resolveBranchRemote(ctx context.Context, dir, branch string) string {
	remote, err := resolvePushRemote(ctx, ws.runner, dir, branch, "")
	if err != nil || remote == "" {
		return "origin"
	}
	return remote
}

// isStrictlyBehind reports whether HEAD is an ancestor of ref (i.e. a
// fast-forward pull is possible). ref is the trigger SHA (see branchSyncedFinding);
// classifying against it — rather than the local <remote>/<branch> tracking ref —
// keeps the trigger and the classification on one source. If ref's object is not
// present locally (a failed/absent best-effort fetch leaves it so for the behind
// case) merge-base errors and this returns false: the finding then falls through
// to the diverged/manual arm rather than mis-firing a fast-forward.
func (ws *Workspace) isStrictlyBehind(ctx context.Context, dir, ref string) bool {
	_, err := ws.runner.Run(ctx, "git",
		[]string{"-C", dir, "merge-base", "--is-ancestor", "HEAD", ref}, exec.RunOptions{})
	return err == nil
}

// isStrictlyAhead reports whether ref is an ancestor of HEAD (i.e. a fast-forward
// push is possible — local is ahead, not diverged). ref is the trigger SHA. For a
// genuinely-ahead repo ref is reachable from HEAD, so its object is present
// locally and this holds even with no prior fetch — which is exactly why the
// ahead case is robust against a stale/failed fetch.
func (ws *Workspace) isStrictlyAhead(ctx context.Context, dir, ref string) bool {
	_, err := ws.runner.Run(ctx, "git",
		[]string{"-C", dir, "merge-base", "--is-ancestor", ref, "HEAD"}, exec.RunOptions{})
	return err == nil
}

// checkBranchUniform (worktree mode) verifies all present members share one
// branch name; a member on a different branch is a branch-uniform error, and a
// uniform branch that differs from the set-dir name is a naming-hygiene warning.
func (ws *Workspace) checkBranchUniform(ctx context.Context, present map[string]string) []Finding {
	branches := map[string]string{} // repo -> branch
	counts := map[string]int{}
	for name, dir := range present {
		cur, detached, _, ok := ws.branchInfo(ctx, dir)
		if !ok || detached {
			cur = "(detached)"
		}
		branches[name] = cur
		counts[cur]++
	}
	if len(counts) <= 1 {
		// uniform; optionally compare to set-dir name
		setName := filepath.Base(ws.root)
		for _, b := range branches {
			if b != setName && b != "(detached)" {
				return []Finding{{
					CheckID: "branch-uniform", Severity: SevWarning,
					Message: fmt.Sprintf("worktree members are on %q but the set dir is %q", b, setName),
				}}
			}
			break
		}
		return nil
	}
	var fs []Finding
	for name, b := range branches {
		fs = append(fs, Finding{
			CheckID: "branch-uniform", Repo: name, Severity: SevError,
			Message: fmt.Sprintf("worktree member %q is on %q; members must share one branch", name, b),
			Manual:  fmt.Sprintf("git -C %s switch <set-branch>", filepath.Join(ws.root, name)),
		})
	}
	return fs
}

// presentRepoDirs returns configured repos that exist as git repos on disk.
func (ws *Workspace) presentRepoDirs() map[string]string {
	out := map[string]string{}
	for name := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if isGitRepo(dir) {
			out[name] = dir
		}
	}
	return out
}

func branchOrDetached(cur string, detached bool) string {
	if detached {
		return "(detached HEAD)"
	}
	return cur
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
