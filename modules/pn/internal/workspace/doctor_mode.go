// internal/workspace/doctor_mode.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// workspaceMode reports "worktree" when the resolved root's member checkouts
// are linked git worktrees, else "primary". Detection is intentionally behind
// this one function so the signal can change later.
//
// Signal: for a linked worktree, `git rev-parse --git-common-dir` points at the
// canonical repo's .git (outside this checkout), whereas for a normal clone it
// resolves to this checkout's own ".git". A submodule would also have a .git
// FILE, so we compare common-dir vs git-dir rather than stat'ing .git.
func (ws *Workspace) workspaceMode(ctx context.Context) string {
	for name := range ws.config.Repos {
		dir := filepath.Join(ws.root, name)
		if !dirExists(dir) {
			continue
		}
		// Resolve dir's symlinks once, up front, and use the resolved form for
		// BOTH probes below. x/gitclient's Locator.CommonDir anchors itself at
		// dir's symlink-resolved absolute path (design section 4.4 D2) before
		// computing commonDir, whereas the raw --git-dir probe below runs
		// `-C dir` unresolved; comparing the two against a single resolved base
		// keeps them on the same footing. Without this, a symlinked temp root
		// (e.g. macOS's /var -> /private/var, which t.TempDir() returns)
		// makes commonDir and absUnder(dir, gitDir) disagree on spelling for the
		// SAME real path, always reporting "worktree" (verified via
		// TestWorkspaceMode_Primary regressing without this resolve).
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			resolvedDir = dir
		}
		gitDir := ws.gitRevParse(ctx, resolvedDir, "--git-dir")
		commonDir := ws.gitCommonDir(ctx, resolvedDir)
		if gitDir == "" || commonDir == "" {
			continue
		}
		if absUnder(resolvedDir, gitDir) != commonDir {
			return "worktree"
		}
	}
	return "primary"
}

// gitRevParse runs `git -C dir rev-parse <flag>` via the raw runner. --git-dir
// has no x/gitclient equivalent (bead pg2-oxle0's scope is Locator/RefReader
// only), so it stays here unmigrated.
func (ws *Workspace) gitRevParse(ctx context.Context, dir, flag string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", flag}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// gitCommonDir returns dir's git-common-dir via x/gitclient's Locator.CommonDir
// (bead pg2-oxle0). Locator.CommonDir runs `rev-parse --path-format=absolute
// --git-common-dir` -- already absolute, unlike gitRevParse's raw
// `--git-common-dir` (which absUnder must still resolve relative to dir) --
// so the caller must NOT re-run absUnder on this result. This mirrors the
// documented pa-monitor migration note (design section 4.2 item (a)): the
// pre-migration call here was the same relative-or-absolute
// `rev-parse --git-common-dir` form; CommonDir's --path-format=absolute is a
// deliberate, low-risk behavior firming-up, not a functional change for any
// real repo layout.
func (ws *Workspace) gitCommonDir(ctx context.Context, dir string) string {
	client, err := openGitReader(ctx, dir)
	if err != nil {
		return ""
	}
	commonDir, err := client.CommonDir(ctx)
	if err != nil {
		return ""
	}
	return commonDir
}

// absUnder resolves p relative to base and returns the cleaned absolute path,
// so a relative ".git" and an absolute common-dir can be compared.
func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}
