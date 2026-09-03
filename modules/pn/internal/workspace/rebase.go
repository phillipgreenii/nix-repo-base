package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/phillipgreenii/x/gitclient"
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

// resolveRef reports whether ref resolves in repoDir.
//
// Migrated onto x/gitclient's RefReader.RefExists (bead pg2-oxle0): the
// pre-migration call was `rev-parse --verify --quiet <ref>` (no `^{commit}`
// suffix), whereas RefExists runs `rev-parse --verify --quiet <ref>^{commit}`.
// Onto is always a branch-shaped ref ("main", "origin/main", or another local
// ref -- never a tag/blob/tree), and a branch ref always resolves to a
// commit, so the `^{commit}` dereference is a no-op for every value this
// function is actually called with.
func (ws *Workspace) resolveRef(ctx context.Context, repoDir, ref string) bool {
	client, err := openGitReader(ctx, repoDir)
	if err != nil {
		return false
	}
	ok, err := client.RefExists(ctx, ref)
	return err == nil && ok
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
//
// Migrated onto x/gitclient's Fetcher.Fetch and Syncer.Sync (bead pg2-8bfb5,
// design pg2-migib §7a): both stream to out/errOut via Handle.AttachStream —
// the operator watches fetch/rebase progress live, exactly as the raw runner
// did before this migration.
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
			client, err := openGitMutator(ctx, repoDir)
			if err != nil {
				return fmt.Errorf("git rebase --autostash %s in %s: %w", opts.Onto, name, err)
			}
			h, err := client.Sync(ctx, gitclient.SyncOptions{Onto: opts.Onto})
			if err != nil {
				return fmt.Errorf("git rebase --autostash %s in %s: %w", opts.Onto, name, err)
			}
			h.AttachStream(out, out)
			if err := h.Wait(); err != nil {
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
		client, err := openGitMutator(ctx, repoDir)
		if err != nil {
			return fmt.Errorf("git fetch in %s: %w", name, err)
		}
		fh, err := client.Fetch(ctx, gitclient.FetchOptions{})
		if err != nil {
			return fmt.Errorf("git fetch in %s: %w", name, err)
		}
		fh.AttachStream(out, out)
		if err := fh.Wait(); err != nil {
			return fmt.Errorf("git fetch in %s: %w", name, err)
		}
		sh, err := client.Sync(ctx, gitclient.SyncOptions{})
		if err != nil {
			return fmt.Errorf("git pull --rebase --autostash in %s: %w", name, err)
		}
		sh.AttachStream(out, out)
		if err := sh.Wait(); err != nil {
			return fmt.Errorf("git pull --rebase --autostash in %s: %w", name, err)
		}
	}
	return nil
}
