package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// RebaseOptions configures Rebase.
type RebaseOptions struct{}

// Rebase runs `git mu` (custom user alias for maintenance/update — typically
// pull --rebase --autostash) in each workspace repo that has a configured
// upstream, streaming output to out. Repos without an upstream are skipped.
func (ws *Workspace) Rebase(ctx context.Context, out io.Writer, opts RebaseOptions) error {
	names := orderedRepoNames(ws.config.Repos)
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
