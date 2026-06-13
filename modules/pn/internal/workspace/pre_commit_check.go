package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// PreCommitCheckOptions configures PreCommitCheck.
type PreCommitCheckOptions struct {
	Terminal string // overrides workspace.terminal for this invocation
}

// PreCommitCheck runs `pre-commit run --all-files` in each workspace repo,
// streaming each run's output to out. Matches the bash version which does NOT
// abort on per-repo failure; we mirror that by collecting failures and
// returning a combined error at the end.
func (ws *Workspace) PreCommitCheck(ctx context.Context, out io.Writer, opts PreCommitCheckOptions) error {
	names := orderedRepoNames(ws.config.Repos)
	var firstErr error
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		fmt.Fprintf(out, "  --== pre-commit %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "pre-commit", []string{"run", "--all-files"}, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pre-commit in %s: %w", name, err)
			}
		}
	}
	return firstErr
}
