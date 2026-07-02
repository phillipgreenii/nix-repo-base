// internal/workspace/doctor_refrev.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// resolveRefRevs computes the reference rev for each configured repo.
//
//	primary  : remote default-branch HEAD via `git ls-remote <url> refs/heads/<branch>`,
//	           plus a best-effort `git fetch <remote>` (remote resolved via the
//	           push-convention chain) so fastForwardIfBehind can run.
//	           offline or unresolvable remote -> skipped[repo]=true.
//	worktree : the member checkout's committed HEAD (captureHead); never skipped.
func (ws *Workspace) resolveRefRevs(ctx context.Context, mode string, offline bool) (map[string]string, map[string]bool) {
	refRev := make(map[string]string, len(ws.config.Repos))
	skipped := make(map[string]bool)

	for name, rc := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if mode == "worktree" {
			if !dirExists(dir) {
				continue
			}
			if sha, err := captureHead(ctx, ws.runner, dir); err == nil {
				refRev[name] = sha
			}
			continue
		}
		// primary
		if offline {
			skipped[name] = true
			continue
		}
		url := displayURL(rc)
		branch := rc.Branch
		if branch == "" {
			branch = "main"
		}
		sha := ws.lsRemoteHead(ctx, url, branch)
		if sha == "" {
			skipped[name] = true
			continue
		}
		refRev[name] = sha
		// Best-effort fetch so <remote>/<branch> exists locally for the
		// fast-forward-pull fix (the behind case needs the remote ref object).
		//
		// Two-source note: refRev[name] above is a LIVE `git ls-remote` head; this
		// fetch is a SEPARATE, best-effort operation whose error is intentionally
		// discarded (offline/transient). Because the source of truth for
		// branch-synced CLASSIFICATION is refRev[name] (not the fetched tracking
		// ref — see branchSyncedFinding), a stale/failed fetch here cannot cause a
		// misclassification; at worst the fast-forward-pull fix has no local ref to
		// merge and fails safely (surfacing as fix-failed). The fetch uses the same
		// resolved canonical remote the fix/push will use, not a hardcoded "origin".
		if dirExists(dir) {
			remote := ws.resolveBranchRemote(ctx, dir, branch)
			_, _ = ws.runner.Run(ctx, "git", []string{"-C", dir, "fetch", "-q", remote, branch}, exec.RunOptions{})
		}
	}
	return refRev, skipped
}

// lsRemoteHead returns the sha that refs/heads/<branch> points to at url, or "".
func (ws *Workspace) lsRemoteHead(ctx context.Context, url, branch string) string {
	if url == "" {
		return ""
	}
	res, err := ws.runner.Run(ctx, "git",
		[]string{"ls-remote", url, "refs/heads/" + branch}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(res.Stdout))
	if line == "" {
		return ""
	}
	return strings.Fields(line)[0]
}
