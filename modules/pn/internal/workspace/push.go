package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// PushOptions configures Push.
type PushOptions struct{}

// hasUpstream checks whether the branch at repoDir has a configured upstream.
// Mirrors bash workspace_has_upstream (git rev-parse --abbrev-ref @{u}).
func (ws *Workspace) hasUpstream(ctx context.Context, repoDir string) bool {
	_, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "@{u}"}, exec.RunOptions{})
	return err == nil
}

// Push runs `git push` in each workspace repo that has a configured upstream,
// streaming push output to out. Warning output goes to errOut (stderr). Repos
// without an upstream branch are skipped. Repos are processed in topological
// order (dependencies before consumers).
// Push is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Push(ctx context.Context, out io.Writer, errOut io.Writer, opts PushOptions) error {
	if ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if !ws.hasUpstream(ctx, repoDir) {
			continue
		}
		fmt.Fprintf(out, "  --== push %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git push in %s: %w", name, err)
		}
	}
	return nil
}
