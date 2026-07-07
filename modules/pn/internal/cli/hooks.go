package cli

import (
	"context"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// runWithHooks executes fn surrounded by the workspace's event hooks for the
// named pn-workspace command.
//
// TODO(pg2-5yq5): wire this to workspace + per-repo event dispatch
// (w.RunEventHooks over w.ProcessedReposFor) in Task 9. Stubbed to `return
// fn()` to keep the map->slice hooks reshape batch (Task 3) compiling; hooks do
// not fire until Task 9 lands. The context/workspace imports remain used via
// the signature.
func runWithHooks(ctx context.Context, w *workspace.Workspace, name string, fn func() error) error {
	return fn()
}
