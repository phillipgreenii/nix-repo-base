package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ApplyOptions configures Apply.
type ApplyOptions struct {
	// ApplyCmd overrides the apply command template (currently unused;
	// see TODO below).
	ApplyCmd string
}

// Apply runs `nix fmt` then a rebuild command across each workspace repo.
//
// TODO(tc-perh.5): port full pn-workspace-apply.sh semantics: identify the
// terminal flake, inject --override-input for every non-terminal repo,
// honor apply_command template from pn-workspace.toml, run nvd-diff against
// the previous system profile, and integrate ul_check_nix_daemon /
// ul_needs_rebuild. The current implementation runs `nix fmt` + a placeholder
// rebuild per repo, sufficient for unit-test scaffolding.
func (ws *Workspace) Apply(ctx context.Context, opts ApplyOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: repoDir}); err != nil {
			return fmt.Errorf("nix fmt in %s: %w", name, err)
		}
		if _, err := ws.runner.Run(ctx, "nix", []string{"build", "."}, exec.RunOptions{Dir: repoDir}); err != nil {
			return fmt.Errorf("apply build in %s: %w", name, err)
		}
	}
	return nil
}
