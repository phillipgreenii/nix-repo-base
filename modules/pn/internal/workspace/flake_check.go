package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// FlakeCheckOptions configures FlakeCheck.
type FlakeCheckOptions struct{}

// FlakeCheck runs `nix flake check` in every workspace repo. Per-repo failures
// are collected; the overall call returns non-nil if any failed. Matches the
// bash "full sweep" behavior — does not short-circuit on first failure.
//
// TODO(tc-perh.5): the bash version invokes via pn-ws-nix which injects
// --override-input flags. The Go port runs bare `nix flake check` for
// simplicity; integration tests will catch any cases where the missing
// overrides matter.
func (ws *Workspace) FlakeCheck(ctx context.Context, opts FlakeCheckOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	var failed []string
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if _, err := ws.runner.Run(ctx, "nix", []string{"flake", "check"}, exec.RunOptions{Dir: repoDir}); err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("flake check failed in %d project(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}
