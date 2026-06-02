package workspace

import (
	"context"
	"fmt"
	"io"
)

// UpgradeOptions configures Upgrade.
type UpgradeOptions struct {
	// ApplyCmd is forwarded to Apply.
	ApplyCmd string
}

// Upgrade runs Update followed by Apply. Equivalent to the bash one-liner
// `pn-workspace-update && pn-workspace-apply`.
func (ws *Workspace) Upgrade(ctx context.Context, opts UpgradeOptions) error {
	if err := ws.Update(ctx, UpdateOptions{Recreate: true}); err != nil {
		return fmt.Errorf("upgrade: update: %w", err)
	}
	if err := ws.Apply(ctx, io.Discard, ApplyOptions{ApplyCmd: opts.ApplyCmd}); err != nil {
		return fmt.Errorf("upgrade: apply: %w", err)
	}
	return nil
}
