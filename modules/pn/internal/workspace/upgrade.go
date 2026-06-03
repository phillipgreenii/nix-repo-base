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
// `pn-workspace-update && pn-workspace-apply`. Apply progress is written to out.
func (ws *Workspace) Upgrade(ctx context.Context, out io.Writer, opts UpgradeOptions) error {
	if err := ws.Update(ctx, out, UpdateOptions{Recreate: true}); err != nil {
		return fmt.Errorf("upgrade: update: %w", err)
	}
	if err := ws.Apply(ctx, out, ApplyOptions{ApplyCmd: opts.ApplyCmd}); err != nil {
		return fmt.Errorf("upgrade: apply: %w", err)
	}
	return nil
}
