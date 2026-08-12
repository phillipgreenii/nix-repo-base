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
	// with `git push -u <remote> <current-branch>`, recording the upstream.
	// Without this flag, repos with no upstream are silently skipped.
	SetUpstream bool
	// Remote, when non-empty, overrides remote resolution for every repo when
	// SetUpstream is true. Equivalent to passing --remote <name> on the CLI.
	Remote string
	// NoSiblings opts OUT of the workspace-sibling relock that Push performs
	// between pushes (ADR 0023 item 3, `--no-siblings`): a plain publish with no
	// lock propagation. The zero value therefore PROPAGATES, matching the CLI
	// default — a programmatic caller that wants the documented `push` behavior
	// need not set anything.
	NoSiblings bool
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

// resolvePushRemote returns the remote name to use for `git push -u` in repoDir.
//
// Resolution chain (highest precedence first):
//  1. flagOverride: if non-empty, use it (error if the named remote doesn't exist).
//  2. Single-remote shortcut: if exactly one remote, use it.
//  3. git config branch.<branch>.pushRemote (per-branch push remote).
//  4. git config --local remote.pushDefault (repo-local default).
//  5. git config --global remote.pushDefault (user-global default).
//  6. "origin" if among the repo's remotes.
//  7. Error: emit a structured message naming available remotes and hint commands.
func resolvePushRemote(
	ctx context.Context,
	runner exec.Runner,
	repoDir string,
	branch string,
	flagOverride string,
) (string, error) {
	// Fetch the full remote list once; used in steps 1, 2, 6.
	remotesRes, err := runner.Run(ctx, "git", []string{"-C", repoDir, "remote"}, exec.RunOptions{})
	if err != nil {
		return "", fmt.Errorf("git remote in %s: %w", repoDir, err)
	}
	remoteLines := strings.TrimSpace(string(remotesRes.Stdout))
	var remotes []string
	if remoteLines != "" {
		remotes = strings.Split(remoteLines, "\n")
	}

	hasRemote := func(name string) bool {
		for _, r := range remotes {
			if strings.TrimSpace(r) == name {
				return true
			}
		}
		return false
	}

	// Step 1: explicit flag override.
	if flagOverride != "" {
		if !hasRemote(flagOverride) {
			return "", fmt.Errorf("remote %q does not exist in %s (available: %s)",
				flagOverride, repoDir, strings.Join(remotes, ", "))
		}
		return flagOverride, nil
	}

	// Step 2: single-remote shortcut.
	if len(remotes) == 1 {
		return strings.TrimSpace(remotes[0]), nil
	}

	// Step 3: git config branch.<branch>.pushRemote
	if branch != "" {
		res, err := runner.Run(ctx, "git", []string{"-C", repoDir, "config", "--get", "branch." + branch + ".pushRemote"}, exec.RunOptions{})
		if err == nil {
			if v := strings.TrimSpace(string(res.Stdout)); v != "" {
				return v, nil
			}
		}
	}

	// Step 4: git config --local remote.pushDefault
	res, err := runner.Run(ctx, "git", []string{"-C", repoDir, "config", "--local", "--get", "remote.pushDefault"}, exec.RunOptions{})
	if err == nil {
		if v := strings.TrimSpace(string(res.Stdout)); v != "" {
			return v, nil
		}
	}

	// Step 5: git config --global remote.pushDefault
	res, err = runner.Run(ctx, "git", []string{"-C", repoDir, "config", "--global", "--get", "remote.pushDefault"}, exec.RunOptions{})
	if err == nil {
		if v := strings.TrimSpace(string(res.Stdout)); v != "" {
			return v, nil
		}
	}

	// Step 6: "origin" if present.
	if hasRemote("origin") {
		return "origin", nil
	}

	// Step 7: structured error.
	return "", fmt.Errorf(
		"cannot determine push remote for %s (available remotes: %s); "+
			"set one with `git config remote.pushDefault <name>` or pass `--remote <name>`",
		repoDir, strings.Join(remotes, ", "),
	)
}

// relockSiblingsBeforePush relocks repoDir's workspace-sibling flake inputs to
// their upstreams' current REMOTE revs and commits the bump, immediately before
// this repo is pushed. It is the per-repo half of the interleaved
// push-then-propagate loop Push owns (ADR 0023 item 2).
//
// Ordering: Push walks repos in TOPOLOGICAL order, so by the time a consumer is
// reached every workspace dependency of it has already been pushed in this same
// run. Relocking the consumer here therefore resolves its upstreams' *new*
// remote tips — which is the same sequence as "push A, then relock A's
// consumers, then push them", expressed as one pass instead of two. C1 is why
// the order cannot be inverted: propagateWorkspaceEdges resolves each alias
// against its DECLARED flake URL (`nix flake update --refresh <alias>`), so a
// consumer can only ever relock to a rev that is ALREADY on the remote.
//
// A repo with no workspace-sibling inputs returns before any subprocess runs, so
// propagation adds zero cost (and zero calls) to an edgeless workspace.
//
// The dirty-tree refusal is deliberate: the relock ends in a `git commit`, and
// the canonical clone — unlike update's throwaway worktree — is a place a person
// keeps work. Committing a lock bump on top of someone's staged changes would
// sweep them into a "chore(deps): bump" commit. Failing loudly (naming
// --no-siblings) is preferred over silently skipping the relock, which would
// reproduce the very failure mode ADR 0023 exists to prevent: publishing that
// looks like it converged the locks but did not.
func (ws *Workspace) relockSiblingsBeforePush(ctx context.Context, out io.Writer, name, repoDir string, aliases []string) error {
	if len(aliases) == 0 {
		return nil
	}
	// A configured repo that is not cloned yet has nothing to relock. Return
	// before the dirty probe: `git -C <missing> diff --quiet` exits 128, which
	// isDirty correctly reports as an indeterminate probe and would turn a repo
	// `push` has always simply skipped (no upstream) into a hard failure.
	if !isGitRepo(repoDir) {
		return nil
	}
	dirty, err := ws.isDirty(ctx, repoDir)
	if err != nil {
		return fmt.Errorf("push: %s: could not determine whether the working tree is clean: %w "+
			"(re-run with --no-siblings to publish without relocking workspace siblings)", name, err)
	}
	if dirty {
		return fmt.Errorf("push: %s has uncommitted changes; the workspace-sibling relock ends in a commit and "+
			"will not run on a dirty tree — commit or stash them, or re-run with --no-siblings to publish "+
			"without relocking siblings", name)
	}
	if _, err := ws.propagateWorkspaceEdges(ctx, out, name, repoDir, ws.resolveFlakePath(name), aliases); err != nil {
		return fmt.Errorf("push: %s: relock workspace siblings: %w", name, err)
	}
	return nil
}

// Push publishes the workspace: for each repo in topological order it relocks
// that repo's workspace-sibling flake inputs against their upstreams' current
// remote tips (committing any bump), then runs `git push`. Push OWNS this
// interleaved propagation — `pn workspace update` does neither half (ADR 0023).
// Pass NoSiblings to publish without the relock.
//
// Push output streams to out; warnings go to errOut (stderr). Repos without an
// upstream branch are skipped for the push itself unless SetUpstream is true, in
// which case they get `git push -u <remote> <current-branch>` where <remote> is
// resolved via the convention-based chain (see resolvePushRemote). A skipped
// push does NOT skip the relock: the bump commit is valid local work that a
// later push publishes, and a consumer relocking against an unpushed
// dependency's remote simply sees no change (C1).
//
// Everything runs in the CANONICAL clone — no `git worktree add` is on this
// path, so the generated `.pre-commit-config.yaml` symlink is present and the
// prek pre-push hook finds its config (ADR 0023 item 4).
//
// Push is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Push(ctx context.Context, out io.Writer, errOut io.Writer, opts PushOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)

	// Resolve whether this run propagates, once. Inside a coordinated workforest
	// set, push publishes the set's shared feature branch; relocking siblings
	// there would commit remote-resolved lock bumps onto that branch while the
	// set deliberately validates through `--override-input`, and a subset set has
	// its excluded edges dropped from the lock outright. Propagation is a
	// canonical-clone operation (ADR 0023 item 4), so it is skipped in a set —
	// audibly, since a silent skip is indistinguishable from a converged run.
	propagate := !opts.NoSiblings
	if propagate && ws.inWorkforest() {
		fmt.Fprintln(errOut, "pn: push: inside a coordinated workforest set — publishing branches only, "+
			"no workspace-sibling relock (run `pn workspace push` from the canonical root to propagate)")
		propagate = false
	}
	// Derive the edge lock once, and only when it will be read: effectiveLock
	// falls back to a nix eval per repo when the disk lock is absent or stale, so
	// --no-siblings stays a pure git command. ws.lock is deliberately NOT used
	// directly — it is empty on a fresh/stale checkout and would silently skip
	// every repo (the C3 hazard propagate.go documents).
	var edgeLock *Lock
	if propagate {
		edgeLock, _, _ = ws.effectiveLock(ctx)
	}

	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		if propagate {
			if err := ws.relockSiblingsBeforePush(ctx, out, name, repoDir, workspaceAliasesFromLock(edgeLock, name)); err != nil {
				return err
			}
		}
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
		remote, err := resolvePushRemote(ctx, ws.runner, repoDir, branch, opts.Remote)
		if err != nil {
			fmt.Fprintf(errOut, "pn: push skipped %s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "  --== push %s ==--  \n", name)
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "push", "-u", remote, branch}, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("git push -u %s %s in %s: %w", remote, branch, name, err)
		}
	}
	return nil
}
