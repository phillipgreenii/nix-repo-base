package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// PreCommitCheckOptions configures PreCommitCheck.
type PreCommitCheckOptions struct{}

// PreCommitCheck runs `pre-commit run --all-files` in each workspace repo.
// Matches the bash version which does NOT abort on per-repo failure; we mirror
// that by collecting failures and returning a combined error at the end.
func (ws *Workspace) PreCommitCheck(ctx context.Context, opts PreCommitCheckOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	var firstErr error
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if _, err := ws.runner.Run(ctx, "pre-commit", []string{"run", "--all-files"}, exec.RunOptions{Dir: repoDir}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pre-commit in %s: %w", name, err)
			}
		}
	}
	return firstErr
}
