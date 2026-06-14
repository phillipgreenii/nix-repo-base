package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// RebaseOptions configures Rebase.
type RebaseOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
}

// Rebase runs `git mu` (custom user alias for maintenance/update — typically
// pull --rebase --autostash) in each workspace repo that has a configured
// upstream, streaming output to out. Warning output goes to errOut (stderr).
// Repos without an upstream are skipped. Repos are processed in topological
// order (dependencies before consumers).
// Rebase is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Rebase(ctx context.Context, out io.Writer, errOut io.Writer, opts RebaseOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if !ws.hasUpstream(ctx, repoDir) {
			continue
		}
		fmt.Fprintf(out, "  --== rebase %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "mu"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git mu in %s: %w", name, err)
		}
	}
	return nil
}
