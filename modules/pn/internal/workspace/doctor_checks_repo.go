// internal/workspace/doctor_checks_repo.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// checkRepos audits config↔disk agreement: missing repos, present-but-not-git
// dirs, extra on-disk repos, and origin/url identity.
func (ws *Workspace) checkRepos(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding

	// 1. Configured repos: present? a git repo? identity matches?
	for name, rc := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		switch {
		case !dirExists(dir):
			sev := SevWarning
			msg := fmt.Sprintf("repo %q is not cloned (its override is skipped; build falls back to flake.lock)", name)
			if name == env.terminal {
				sev = SevError
				msg = fmt.Sprintf("terminal repo %q is not cloned; apply/build cannot target it", name)
			}
			// Fix attached once, after the loop: Clone clones ALL missing repos in
			// a single call, so one fix suffices for every repos-present finding.
			fs = append(fs, Finding{
				CheckID: "repos-present", Repo: name, Severity: sev, Message: msg, Fixable: true,
				Manual: "pn workspace clone",
			})
		case !isGitRepo(dir):
			fs = append(fs, Finding{
				CheckID: "repo-is-git", Repo: name, Severity: SevError,
				Message: fmt.Sprintf("repo %q exists on disk but is not a git repo", name),
				Manual:  fmt.Sprintf("rm -rf %s && pn workspace clone", dir),
			})
		default:
			if f := ws.checkRepoIdentity(ctx, name, rc, dir); f != nil {
				fs = append(fs, *f)
			}
		}
	}

	// Attach the single Clone fix to the FIRST repos-present finding. Clone clones
	// every missing repo in one call, so one fix closure repairs them all; the
	// remaining findings stay Fixable (rendered/planned as such) and clear via the
	// residual re-run after the clone.
	for i := range fs {
		if fs[i].CheckID == "repos-present" {
			fs[i].fix = func(c context.Context) error { return ws.Clone(c, os.Stderr, CloneOptions{}) }
			break
		}
	}

	// 2. Extra on-disk repos not in config.
	fs = append(fs, ws.checkExtraRepos()...)
	return fs
}

func (ws *Workspace) checkRepoIdentity(ctx context.Context, name string, rc RepoConfig, dir string) *Finding {
	remotes, err := readGitRemotes(ctx, ws.runner, dir)
	if err != nil {
		return nil // tolerate; readGitRemotes already degrades gracefully
	}
	if err := checkRemoteAgreement(name, rc, remotes); err != nil {
		return &Finding{
			CheckID: "repo-identity", Repo: name, Severity: SevError,
			Message: err.Error(),
			Manual:  fmt.Sprintf("align the remote or pn-workspace.toml, e.g.:  git -C %s remote set-url origin %s", dir, displayURL(rc)),
		}
	}
	return nil
}

// checkExtraRepos flags on-disk git repos at the workspace root that are not in
// config. Fix: reconcileFromFilesystem (adds git repos with a resolvable origin).
func (ws *Workspace) checkExtraRepos() []Finding {
	entries, err := os.ReadDir(ws.root)
	if err != nil {
		return nil
	}
	var fs []Finding
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, configured := ws.config.Repos[name]; configured {
			continue
		}
		dir := filepath.Join(ws.root, name)
		if !isGitRepo(dir) {
			continue // not a repo; ignore (.worktrees, .beads, docs, etc.)
		}
		fs = append(fs, Finding{
			CheckID: "repos-extra", Repo: name, Severity: SevWarning,
			Message: fmt.Sprintf("git repo %q is on disk but not in pn-workspace.toml", name),
			Fixable: true,
			fix:     func(c context.Context) error { return ws.reconcileFromFilesystem(c) },
			Manual:  "pn workspace init",
		})
	}
	return fs
}
