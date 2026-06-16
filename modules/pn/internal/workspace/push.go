package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// PushOptions configures Push.
type PushOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
	// SetUpstream, when true, causes repos that have no upstream to be pushed
	// with `git push -u origin <current-branch>`, recording the upstream.
	// Without this flag, repos with no upstream are silently skipped.
	SetUpstream bool
}

// hasUpstream checks whether the branch at repoDir has a configured upstream.
// Mirrors bash workspace_has_upstream (git rev-parse --abbrev-ref @{u}).
func (ws *Workspace) hasUpstream(ctx context.Context, repoDir string) bool {
	_, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "@{u}"}, exec.RunOptions{})
	return err == nil
}

// currentBranch returns the short branch name for repoDir using
// `git rev-parse --abbrev-ref HEAD`.
func (ws *Workspace) currentBranch(ctx context.Context, repoDir string) (string, error) {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD in %s: %w", repoDir, err)
	}
	branch := strings.TrimSpace(string(bytes.TrimRight(res.Stdout, "\n")))
	return branch, nil
}

// Push runs `git push` in each workspace repo that has a configured upstream,
// streaming push output to out. Warning output goes to errOut (stderr). Repos
// without an upstream branch are skipped unless SetUpstream is true, in which
// case they get `git push -u origin <current-branch>`. Repos are processed in
// topological order (dependencies before consumers).
// Push is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Push(ctx context.Context, out io.Writer, errOut io.Writer, opts PushOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if ws.hasUpstream(ctx, repoDir) {
			fmt.Fprintf(out, "  --== push %s ==--  \n", name)
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				return fmt.Errorf("git push in %s: %w", name, err)
			}
			continue
		}
		if !opts.SetUpstream {
			continue
		}
		branch, err := ws.currentBranch(ctx, repoDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  --== push %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push", "-u", "origin", branch}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git push -u origin %s in %s: %w", branch, name, err)
		}
	}
	return nil
}
