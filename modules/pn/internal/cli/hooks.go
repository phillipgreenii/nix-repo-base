package cli

import (
	"context"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// runWithHooks executes fn surrounded by the workspace's pre/post hooks for
// the named pn-workspace command. Pre-hook failures abort and propagate;
// fn is not invoked. Post-hooks run regardless of fn's outcome and never
// propagate errors (warn-only per spec §4.1).
func runWithHooks(ctx context.Context, w *workspace.Workspace, name string, fn func() error) error {
	hooks := w.Config().Hooks[name]
	runner := w.Runner()
	root := w.Root()

	if err := workspace.RunHooks(ctx, runner, hooks.Pre, root, workspace.HookPhasePre); err != nil {
		return err
	}
	fnErr := fn()
	_ = workspace.RunHooks(ctx, runner, hooks.Post, root, workspace.HookPhasePost)
	return fnErr
}
