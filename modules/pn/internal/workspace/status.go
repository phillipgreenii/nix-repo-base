package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Status writes a per-repo git status report to w. Error and warning output
// goes to errOut (stderr). Repos are processed in topological order
// (dependencies before consumers). A repo that fails its status call is
// reported but does not abort the loop.
// Status is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Status(ctx context.Context, w io.Writer, errOut io.Writer) error {
	if ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "status", "--short"}, exec.RunOptions{})
		if err != nil {
			fmt.Fprintf(errOut, "=== %s (error) ===\n", name)
			fmt.Fprintf(errOut, "%s\n", err)
			continue
		}
		fmt.Fprintf(w, "=== %s ===\n", name)
		if len(res.Stdout) == 0 {
			fmt.Fprintln(w, "(clean)")
		} else {
			_, _ = w.Write(res.Stdout)
		}
	}
	return nil
}
