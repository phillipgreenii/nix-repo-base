package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Status writes a per-repo git status report to w. Repos are processed in
// alphabetical order. A repo that fails its status call is reported but does
// not abort the loop.
func (ws *Workspace) Status(ctx context.Context, w io.Writer) error {
	names := orderedRepoNames(ws.config.Repos)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "status", "--short"}, exec.RunOptions{})
		if err != nil {
			fmt.Fprintf(w, "=== %s (error) ===\n", name)
			fmt.Fprintf(w, "%s\n", err)
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
