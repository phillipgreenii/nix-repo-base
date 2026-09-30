package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

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

// refuseIfDivergedAfterFetch reports an error naming repoDir when its current
// branch is BOTH ahead of AND behind its freshly-fetched upstream -- the
// genuinely-diverged case a plain `git pull --rebase --autostash` cannot
// safely resolve on its own (bd tc-0q3cp; see the bd tc-p08nv reasoning this
// duplicates from `pnwf sync-fetch`'s own preflight). It reuses
// aheadBehindCounts (the same primitive `pn workspace status` and doctor's
// branch-synced check already use) rather than a fresh rev-list, matching
// this file's Reuse-First convention.
//
// ok=false (no upstream, or the rev-list query failed for any reason) is
// treated as "cannot tell" and is NOT an error here: aheadBehindCounts already
// documents that ok=false lets the caller decide how to phrase it, and the
// existing caller in this file (the default Rebase loop) phrases it as
// "proceed as before" -- Fetch already ran and hasUpstream already gated
// entry into this branch, so ok=false here only happens if the rev-list
// itself could not run, which the pre-existing pull --rebase step will
// surface on its own if it matters.
func (ws *Workspace) refuseIfDivergedAfterFetch(ctx context.Context, name, repoDir string) error {
	ahead, behind, ok := ws.aheadBehindCounts(ctx, repoDir)
	if !ok {
		return nil
	}
	aheadN, aerr := strconv.Atoi(ahead)
	behindN, berr := strconv.Atoi(behind)
	if aerr != nil || berr != nil || aheadN == 0 || behindN == 0 {
		return nil
	}
	return fmt.Errorf(
		"pn workspace rebase: %s: local branch is both ahead (%s) and behind (%s) its upstream after fetch -- "+
			"a plain `git pull --rebase --autostash` there risks replaying local commits onto upstream content "+
			"that can genuinely conflict (the routine drain-lands-locally-plus-peer-pushes case), and reconciling "+
			"it needs a human decision, not an automatic rebase (R-3). Nothing was rebased in %s. Resolve by hand "+
			"(e.g. `git -C %s log --oneline @{upstream}...HEAD` to see both sides, then typically "+
			"`git -C %s rebase @{upstream}`), then re-run `pn workspace rebase`",
		name, ahead, behind, repoDir, repoDir, repoDir,
	)
}

// Rebase runs git rebase operations across all workspace repos in topological
// order (dependencies before consumers).
//
// Without Onto (default): runs `git fetch` then `git pull --rebase --autostash`
// in each repo that has a configured upstream. Repos without an upstream are
// skipped. Immediately after the fetch, if the repo's branch is now BOTH ahead
// of AND behind its upstream (bd tc-0q3cp, duplicating bd tc-p08nv's
// ahead-and-behind safeguard from `pnwf sync-fetch` down into this command
// itself, so a bare/direct `pn workspace rebase` invocation is protected too --
// not only the pnwf-guided path), the pull is refused: a plain
// `pull --rebase` there would replay local commits onto content that can
// genuinely conflict (the routine drain-lands-locally-plus-peer-pushes case),
// and reconciling that needs a human decision, not an automatic rebase (R-3).
// On this or any other failure the function returns immediately -- repos
// already processed earlier in topo order keep whatever state they ended up
// in; nothing further is touched.
//
// With Onto: runs `git rebase --autostash <Onto>` in each repo, with no
// fetch/pull, and does NOT apply the ahead-and-behind guard above: Onto is
// itself the explicit, operator-directed reconciliation mechanism (e.g.
// `pn workspace rebase --onto origin/main` after resolving a divergence by
// hand), so refusing it on exactly the divergence it exists to resolve would
// defeat its purpose. Repos where the ref does not resolve are skipped with a
// stderr notice; the rest continue (resilient per-repo style).
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
			repoDir := filepath.Join(ws.root, name)
			if !ws.resolveRef(ctx, repoDir, opts.Onto) {
				fmt.Fprintf(errOut, "pn workspace rebase: skipping %s — ref %q not found\n", name, opts.Onto)
				continue
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
		}
		return nil
	}

	// Default: fetch + pull --rebase --autostash onto tracked upstream.
	first := true
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if !ws.hasUpstream(ctx, repoDir) {
			continue
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
		if err := ws.refuseIfDivergedAfterFetch(ctx, name, repoDir); err != nil {
			return err
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
