package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// PushOptions configures Push.
type PushOptions struct{}

// hasUpstream checks whether the branch at repoDir has a configured upstream.
// Mirrors bash workspace_has_upstream (git rev-parse --abbrev-ref @{u}).
func (ws *Workspace) hasUpstream(ctx context.Context, repoDir string) bool {
	_, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "@{u}"}, exec.RunOptions{})
	return err == nil
}

// Push runs `git push` in each workspace repo that has a configured upstream.
// Repos without an upstream branch are skipped.
func (ws *Workspace) Push(ctx context.Context, opts PushOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if !ws.hasUpstream(ctx, repoDir) {
			continue
		}
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{}); err != nil {
			return fmt.Errorf("git push in %s: %w", name, err)
		}
	}
	return nil
}
