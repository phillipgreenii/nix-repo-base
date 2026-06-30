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
		gitDir := ws.gitRevParse(ctx, dir, "--git-dir")
		commonDir := ws.gitRevParse(ctx, dir, "--git-common-dir")
		if gitDir == "" || commonDir == "" {
			continue
		}
		if absUnder(dir, gitDir) != absUnder(dir, commonDir) {
			return "worktree"
		}
	}
	return "primary"
}

func (ws *Workspace) gitRevParse(ctx context.Context, dir, flag string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "rev-parse", flag}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

// absUnder resolves p relative to base and returns the cleaned absolute path,
// so a relative ".git" and an absolute common-dir can be compared.
func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}
