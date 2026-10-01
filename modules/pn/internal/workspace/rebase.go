package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
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

// rebaseInProgress reports whether a rebase is currently in progress in
// repoDir. It asks git where the rebase state directories live
// (`rev-parse --git-path rebase-merge` / `rebase-apply`) rather than
// hardcoding `.git/...`, because a linked worktree keeps that state under
// `.git/worktrees/<n>/`. Both backends are checked: rebase-merge (the merge
// backend, default since git 2.26) and rebase-apply (the older apply/am
// backend). A relative answer is anchored on repoDir, since `-C` made repoDir
// git's cwd and this process's cwd is arbitrary. Modeled on
// pnwf_rebase_in_progress (modules/pnwf/lib/pnwf-lib.bash).
//
// A non-zero git exit comes back from the runner as an error; that is
// returned as err with inProgress=false, meaning "state unknown" -- callers
// decide how to treat it (the pre-probe proceeds, the post-probe does not
// abort).
func (ws *Workspace) rebaseInProgress(ctx context.Context, repoDir string) (inProgress bool, err error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--git-path", name}, exec.RunOptions{})
		if err != nil {
			return false, err
		}
		path := strings.TrimSpace(string(res.Stdout))
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoDir, path)
		}
		if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// abortRebase runs `git rebase --abort` in repoDir. A nil return means git
// reported the abort succeeded; only then may callers claim "rolled back".
func (ws *Workspace) abortRebase(ctx context.Context, repoDir string) error {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rebase", "--abort"}, exec.RunOptions{})
	if err != nil {
		if stderr := strings.TrimSpace(string(res.Stderr)); stderr != "" {
			return fmt.Errorf("%w: %s", err, stderr)
		}
		return err
	}
	return nil
}

// syncDefault runs the default-path `git pull --rebase --autostash` for one
// repo and, if it fails mid-rebase, rolls the repo back. See Rebase's doc
// comment for the contract.
func (ws *Workspace) syncDefault(ctx context.Context, client gitMutator, out io.Writer, name, repoDir string) error {
	// Pre-probe: never abort a rebase the operator started themselves.
	if inProgress, err := ws.rebaseInProgress(ctx, repoDir); err == nil && inProgress {
		return fmt.Errorf("rebase already in progress in %s (%s); finish or abort it, then re-run `pn workspace rebase`", name, repoDir)
	}
	// A probe error is "state unknown": proceed as before.

	sh, err := client.Sync(ctx, gitclient.SyncOptions{})
	if err != nil {
		// The process did not start; nothing to roll back.
		return fmt.Errorf("git pull --rebase --autostash in %s: %w", name, err)
	}
	sh.AttachStream(out, out)
	syncErr := sh.Wait()
	if syncErr == nil {
		return nil
	}
	wrapped := fmt.Errorf("git pull --rebase --autostash in %s: %w", name, syncErr)
	if ctx.Err() != nil {
		// Cancelled: do not touch the repo further.
		return fmt.Errorf("%w (interrupted: %s may be mid-rebase and may hold an autostash; "+
			"check `git -C %s status` and `git -C %s stash list`)", wrapped, name, repoDir, repoDir)
	}
	inProgress, probeErr := ws.rebaseInProgress(ctx, repoDir)
	if probeErr != nil || !inProgress {
		// No rebase state (network/hook/refusal) or state unknown: report the
		// original failure unchanged.
		return wrapped
	}
	// The pre-probe saw no rebase, so this attempt created the one now in
	// progress: roll it back so "nothing was rebased" holds.
	if abortErr := ws.abortRebase(ctx, repoDir); abortErr != nil {
		return fmt.Errorf("%w; AND `git rebase --abort` failed (%v): %s is STILL mid-rebase and may hold an "+
			"autostash -- inspect with `git -C %s status` and `git -C %s stash list`, then finish with "+
			"`git -C %s rebase --continue` or `git -C %s rebase --abort`",
			wrapped, abortErr, name, repoDir, repoDir, repoDir, repoDir)
	}
	return fmt.Errorf("%w; the rebase conflicted and was rolled back: nothing was rebased in %s "+
		"(repos earlier in topological order stay rebased). Resolve by hand "+
		"(`git -C %s log --oneline @{upstream}...HEAD` to see both sides, then `git -C %s rebase @{upstream}`) "+
		"and re-run `pn workspace rebase`",
		wrapped, name, repoDir, repoDir)
}

// Rebase runs git rebase operations across all workspace repos in topological
// order (dependencies before consumers).
//
// Without Onto (default): runs `git fetch` then `git pull --rebase --autostash`
// in each repo that has a configured upstream. Repos without an upstream are
// skipped. There is deliberately NO divergence (ahead-and-behind) pre-check:
// a diverged branch often rebases cleanly, so the sync is always attempted and
// only a real failure is an error. If a rebase is already in progress in a
// repo before the sync, Rebase returns an error without running anything and
// without aborting (the operator's own work is never aborted). If the sync
// fails and left a rebase of its own in progress (a conflict), it is rolled
// back with `git rebase --abort`, so "nothing was rebased" holds for that repo
// and the error says so; the error says the repo is STILL mid-rebase only when
// the abort itself failed. If the sync fails with no rebase in progress
// (network, hook, refusal) the original error is returned unchanged; a
// cancelled context is never followed by an abort. On any failure the
// function returns immediately -- repos already processed earlier in topo
// order keep whatever state they ended up in; nothing further is touched.
//
// Known caveats: a rebase that succeeds but whose autostash pop conflicts
// exits 0 and leaves unmerged (UU) files plus a kept stash (see
// pnwf-lib.bash's sync-fetch handling). That is not new for ahead-only or
// behind-only repos and is now also possible for diverged ones; it is not
// detected here. A SIGKILL between the rebase starting and the abort leaves
// the repo mid-rebase.
//
// With Onto: runs `git rebase --autostash <Onto>` in each repo, with no
// fetch/pull and no in-progress probe or rollback: Onto is itself the
// explicit, operator-directed reconciliation mechanism (e.g.
// `pn workspace rebase --onto origin/main` after resolving a divergence by
// hand). Unlike the default path, --onto deliberately still leaves a repo
// mid-rebase on conflict (operator-directed), for the operator to resolve.
// Repos where the ref does not resolve are skipped with a stderr notice; the
// rest continue (resilient per-repo style).
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
		first := true
		for _, name := range names {
			if err := inRepoSpan(ctx, name, func(ctx context.Context) error {
				repoDir := filepath.Join(ws.root, name)
				if !ws.resolveRef(ctx, repoDir, opts.Onto) {
					fmt.Fprintf(errOut, "pn workspace rebase: skipping %s — ref %q not found\n", name, opts.Onto)
					return nil
				}
				// Blank line between repo blocks (not before the first).
				if !first {
					fmt.Fprintln(out)
				}
				first = false
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
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}

	// Default: fetch + pull --rebase --autostash onto tracked upstream.
	first := true
	for _, name := range names {
		if err := inRepoSpan(ctx, name, func(ctx context.Context) error {
			repoDir := filepath.Join(ws.root, name)
			if !ws.hasUpstream(ctx, repoDir) {
				return nil
			}
			// Blank line between repo blocks (not before the first).
			if !first {
				fmt.Fprintln(out)
			}
			first = false
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
			return ws.syncDefault(ctx, client, out, name, repoDir)
		}); err != nil {
			return err
		}
	}
	return nil
}
