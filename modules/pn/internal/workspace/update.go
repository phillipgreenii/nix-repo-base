package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// UpdateOptions configures Update.
type UpdateOptions struct {
	// Recreate forces full lock recreation (currently treated as an
	// indicator for Upgrade; see upgrade.go).
	Recreate bool
}

// Update pulls each workspace repo, runs its ./update-locks.sh, and pushes.
// Repos without an upstream skip pull/push but still attempt update-locks.
// Repos with a dirty working tree are skipped.
//
// TODO(tc-perh.5): port the full pn-workspace-update.sh signal-handling and
// partial-failure aggregation. The current implementation aborts on the
// first error in any step, which is stricter than bash.
func (ws *Workspace) Update(ctx context.Context, opts UpdateOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		// Skip if dirty.
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "diff", "--quiet"}, exec.RunOptions{}); err != nil {
			// dirty — skip
			continue
		}
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "diff", "--cached", "--quiet"}, exec.RunOptions{}); err != nil {
			continue
		}

		hasUp := ws.hasUpstream(ctx, repoDir)
		if hasUp {
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{}); err != nil {
				return fmt.Errorf("git pull in %s: %w", name, err)
			}
		}
		// Run update-locks.sh — it must be present in the repo.
		if _, err := ws.runner.Run(ctx, "./update-locks.sh", nil, exec.RunOptions{Dir: repoDir}); err != nil {
			return fmt.Errorf("update-locks in %s: %w", name, err)
		}
		if hasUp {
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{}); err != nil {
				return fmt.Errorf("git push in %s: %w", name, err)
			}
		}
	}
	return nil
}
