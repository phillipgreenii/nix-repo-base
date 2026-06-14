package workspace

import (
	"context"
	"fmt"
	"io"
)

// UpgradeOptions configures Upgrade.
type UpgradeOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
	// ApplyCmd is forwarded to Apply.
	ApplyCmd string
	// ULLibDir is forwarded to Update (resolve once via ResolveULLibDir).
	ULLibDir string
}

// Upgrade runs Update followed by Apply. Equivalent to the bash one-liner
// `pn-workspace-update && pn-workspace-apply`. Apply progress is written to out.
func (ws *Workspace) Upgrade(ctx context.Context, out io.Writer, opts UpgradeOptions) error {
	if err := ws.Update(ctx, out, UpdateOptions{Terminal: opts.Terminal, Recreate: true, ULLibDir: opts.ULLibDir}); err != nil {
		return fmt.Errorf("upgrade: update: %w", err)
	}
	if err := ws.Apply(ctx, out, ApplyOptions{Terminal: opts.Terminal, ApplyCmd: opts.ApplyCmd}); err != nil {
		return fmt.Errorf("upgrade: apply: %w", err)
	}
	return nil
}
