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

// preCommitCheckCommand picks the command PreCommitCheck runs in repoDir (dual
// mode, per-clone hook bundle spec 7.1). A repo with no bundle but a usable
// legacy .pre-commit-config.yaml (state legacy) keeps the legacy gate,
// `prek run --all-files`; every other repo, bundle or not, goes through
// `pg-hooks run pre-commit --all-files`, which owns the "no bundle", "broken"
// and "stale" messages and exit codes. `pre-commit` itself is never run: it is
// not on PATH here. A directory that is not a git work tree reads as no state,
// so it goes to pg-hooks, which reports it.
func preCommitCheckCommand(repoDir string) (string, []string) {
	if state, _, err := ReadHookBundleState(repoDir); err == nil && state == HookBundleLegacy {
		return "prek", []string{"run", "--all-files"}
	}
	return "pg-hooks", []string{"run", "pre-commit", "--all-files"}
}

// PreCommitCheck runs the pre-commit stage over all files in each workspace
// repo (`pg-hooks run pre-commit --all-files`, or `prek run --all-files` for a
// legacy repo; see preCommitCheckCommand), streaming each run's output to out. Warning output goes to errOut (stderr).
// Matches the bash version which does NOT abort on per-repo failure; we mirror
// that by collecting failures and returning a combined error at the end. Repos
// are processed in topological order (dependencies before consumers).
// PreCommitCheck is a terminal-optional command: if no terminal is configured
// it emits a warning to errOut and continues.
func (ws *Workspace) PreCommitCheck(ctx context.Context, out io.Writer, errOut io.Writer, opts PreCommitCheckOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	var firstErr error
	first := true
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		// Blank line between repo blocks (not before the first).
		if !first {
			fmt.Fprintln(out)
		}
		first = false
		fmt.Fprintf(out, "  --== pre-commit %s ==--  \n", name)
		cmd, args := preCommitCheckCommand(repoDir)
		if _, err := ws.runner.Run(ctx, cmd, args, exec.RunOptions{Dir: repoDir, Stdout: out, Stderr: out}); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s in %s: %w", cmd, name, err)
			}
		}
	}
	return firstErr
}
