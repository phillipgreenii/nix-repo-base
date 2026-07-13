package cli

import (
	"context"
	"os"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

// runWithHooks executes fn surrounded by the workspace's event hooks for the
// named pn-workspace command. It fires the `pre-<name>` event before fn and the
// `post-<name>` event after, over the repos the command processes. Pre-hook
// failures abort and propagate (fn is not invoked); post-hooks run regardless
// of fn's outcome and never propagate errors (warn-only).
func runWithHooks(ctx context.Context, w *workspace.Workspace, name string, fn func() error) error {
	// Shared Close: every hookable verb routes through here, so closing the
	// workspace once fn (and the post-hooks) have run drains its worker pool for
	// all of them in one place — no per-handler `defer w.Close()` to forget,
	// which previously leaked runtime.NumCPU() goroutines per command
	// (bead pg2-oewgp). These verbs never fan out through the pool (only
	// `discover` does, and it manages its own Close), so this is pure cleanup.
	defer w.Close()
	processed := w.ProcessedReposFor(ctx, name)
	if err := w.RunEventHooks(ctx, workspace.HookPhasePre, name, processed, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fnErr := fn()
	_ = w.RunEventHooks(ctx, workspace.HookPhasePost, name, processed, os.Stdout, os.Stderr)
	return fnErr
}
