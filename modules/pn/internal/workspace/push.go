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
		repoDir, strings.Join(remotes, ", "))
}

// Push runs `git push` in each workspace repo that has a configured upstream,
// streaming push output to out. Warning output goes to errOut (stderr). Repos
// without an upstream branch are skipped unless SetUpstream is true, in which
// case they get `git push -u <remote> <current-branch>` where <remote> is
// resolved via the convention-based chain (see resolvePushRemote). Repos are
// processed in topological order (dependencies before consumers).
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
