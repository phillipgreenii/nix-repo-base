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
	// Onto, when non-empty, rebases each repo's current branch onto this local
	// ref (e.g. "main", "origin/main") instead of the default fetch+pull path.
	// Repos where the ref does not resolve are skipped with a stderr notice.
	Onto string
}

// resolveRef reports whether ref resolves in repoDir using
// `git rev-parse --verify --quiet <ref>`.
func (ws *Workspace) resolveRef(ctx context.Context, repoDir, ref string) bool {
	_, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--verify", "--quiet", ref}, exec.RunOptions{})
	return err == nil
}

// Rebase runs git rebase operations across all workspace repos in topological
// order (dependencies before consumers).
//
// Without Onto (default): runs `git fetch` then `git pull --rebase --autostash`
// in each repo that has a configured upstream. Repos without an upstream are
// skipped. On the first failure the function returns immediately.
//
// With Onto: runs `git rebase --autostash <Onto>` in each repo, with no
// fetch/pull. Repos where the ref does not resolve are skipped with a stderr
// notice; the rest continue (resilient per-repo style).
//
// Rebase is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Rebase(ctx context.Context, out io.Writer, errOut io.Writer, opts RebaseOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)

	if opts.Onto != "" {
		// Local-ref rebase: no fetch/pull; skip repos where ref is absent.
		for _, name := range names {
			repoDir := filepath.Join(ws.root, name)
			if !ws.resolveRef(ctx, repoDir, opts.Onto) {
				fmt.Fprintf(errOut, "pn workspace rebase: skipping %s — ref %q not found\n", name, opts.Onto)
				continue
			}
			fmt.Fprintf(out, "  --== rebase %s ==--  \n", name)
			if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rebase", "--autostash", opts.Onto}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
				return fmt.Errorf("git rebase --autostash %s in %s: %w", opts.Onto, name, err)
			}
		}
		return nil
	}

	// Default: fetch + pull --rebase --autostash onto tracked upstream.
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if !ws.hasUpstream(ctx, repoDir) {
			continue
		}
		fmt.Fprintf(out, "  --== rebase %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "fetch"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git fetch in %s: %w", name, err)
		}
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "pull", "--rebase", "--autostash"}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git pull --rebase --autostash in %s: %w", name, err)
		}
	}
	return nil
}
