package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// FormatOptions configures Format.
type FormatOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
}

// Format runs `nix fmt` in each workspace repo in topological+alphabetical
// order, streaming output to out. Warning output goes to errOut (stderr).
// Format is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
// On the first per-repo failure, Format returns immediately with an error.
func (ws *Workspace) Format(ctx context.Context, out io.Writer, errOut io.Writer, opts FormatOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		fmt.Fprintf(out, "  --== format %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "nix", []string{"fmt"}, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("nix fmt in %s: %w", name, err)
		}
	}
	return nil
}
