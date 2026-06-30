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
		if ws.isDirty(ctx, dir) {
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
				fs = append(fs, Finding{CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote comparison skipped"})
				continue
			}
			ref := env.refRev[name]
			if ref == "" {
				fs = append(fs, Finding{CheckID: "branch-synced", Repo: name, Severity: SevError,
					Skipped: true, Message: "remote rev unresolved (no upstream?)"})
				continue
			}
			local, err := captureHead(ctx, ws.runner, dir)
			if err == nil && local != ref {
				fs = append(fs, ws.branchSyncedFinding(ctx, name, dir, branch, local, ref))
			}
		}
	}
	return fs
}

func (ws *Workspace) branchSyncedFinding(ctx context.Context, name, dir, branch, local, ref string) Finding {
	behind := ws.isStrictlyBehind(ctx, dir, branch)
	f := Finding{
		CheckID: "branch-synced", Repo: name, Severity: SevError,
		Message: fmt.Sprintf("repo %q local HEAD %s != remote %s (%s)", name, short(local), short(ref), ws.aheadBehind(ctx, dir)),
	}
	switch {
	case behind:
		// local HEAD is an ancestor of origin/<branch> → fast-forward pull.
		f.Fixable = true
		f.fix = func(c context.Context) error { return ws.fastForwardIfBehind(c, dir, branch) }
		f.Manual = fmt.Sprintf("git -C %s merge --ff-only origin/%s", dir, branch)
	case ws.isStrictlyAhead(ctx, dir, branch):
		// origin/<branch> is an ancestor of local HEAD → fast-forward push.
		f.Fixable = true
		f.fix = func(c context.Context) error { return ws.pushBranch(c, dir, branch) }
		f.Manual = fmt.Sprintf("git -C %s push origin %s", dir, branch)
	default:
		// genuinely diverged (ahead AND behind) → manual rebase; ff is unsafe.
		f.Manual = fmt.Sprintf("local diverged from origin/%s — resolve by hand:  git -C %s rebase origin/%s", branch, dir, branch)
	}
	return f
}

// isStrictlyBehind reports whether HEAD is an ancestor of origin/<branch>
// (i.e. a fast-forward is possible). Requires a prior fetch (refRev did it).
func (ws *Workspace) isStrictlyBehind(ctx context.Context, dir, branch string) bool {
	_, err := ws.runner.Run(ctx, "git",
		[]string{"-C", dir, "merge-base", "--is-ancestor", "HEAD", "origin/" + branch}, exec.RunOptions{})
	return err == nil
}

// isStrictlyAhead reports whether origin/<branch> is an ancestor of HEAD
// (i.e. a fast-forward push is possible — local is ahead, not diverged).
// Requires a prior fetch (refRev did it).
func (ws *Workspace) isStrictlyAhead(ctx context.Context, dir, branch string) bool {
	_, err := ws.runner.Run(ctx, "git",
		[]string{"-C", dir, "merge-base", "--is-ancestor", "origin/" + branch, "HEAD"}, exec.RunOptions{})
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
				return []Finding{{CheckID: "branch-uniform", Severity: SevWarning,
					Message: fmt.Sprintf("worktree members are on %q but the set dir is %q", b, setName)}}
			}
			break
		}
		return nil
	}
	var fs []Finding
	for name, b := range branches {
		fs = append(fs, Finding{CheckID: "branch-uniform", Repo: name, Severity: SevError,
			Message: fmt.Sprintf("worktree member %q is on %q; members must share one branch", name, b),
			Manual:  fmt.Sprintf("git -C %s switch <set-branch>", filepath.Join(ws.root, name))})
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
