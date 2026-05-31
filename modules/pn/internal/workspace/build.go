package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// BuildOptions configures Build.
type BuildOptions struct {
	// BuildCmd overrides the build command template (currently unused;
	// see TODO below).
	BuildCmd string
}

// Build runs `nix fmt` and `nix build` across each repo in the workspace.
//
// Each `nix build` invocation receives --override-input flags pinning every
// locked workspace repo to its local clone (path:<workspace>/<repo>), so
// inter-repo references resolve to the on-disk sibling rather than the
// upstream flake URL.
//
// TODO: port full pn-workspace-build.sh semantics: pick the terminal flake
// (entry without inputName) and only build that, honor the build_command
// template from pn-workspace.toml. The current implementation runs the
// simpler per-repo loop sufficient for unit-test scaffolding; the
// integration tests in Task 14 are expected to catch behavioral gaps.
func (ws *Workspace) Build(ctx context.Context, opts BuildOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	overrides := computeOverrideArgs(ws)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: repoDir}); err != nil {
			return fmt.Errorf("nix fmt in %s: %w", name, err)
		}
		buildArgs := append([]string{"build"}, overrides...)
		buildArgs = append(buildArgs, ".")
		if _, err := ws.runner.Run(ctx, "nix", buildArgs, exec.RunOptions{Dir: repoDir}); err != nil {
			return fmt.Errorf("nix build in %s: %w", name, err)
		}
	}
	return nil
}
