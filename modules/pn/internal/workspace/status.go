package workspace

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// StatusOptions configures Status.
type StatusOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
}

// Status writes a per-repo git status report to w. Error and warning output
// goes to errOut (stderr). Repos are processed in topological order
// (dependencies before consumers). A repo that fails its status call is
// reported but does not abort the loop.
//
// Each per-repo block reports the checked-out branch (or a detached-HEAD
// indication), the ahead/behind delta of that branch versus its upstream, the
// working-tree status (porcelain, or "(clean)"), the other local branches, and
// the worktrees registered for the repo. The branch/delta/branches/worktree
// queries each degrade gracefully: a failing one is omitted (or reported as
// unknown) rather than aborting the loop.
//
// Status is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Status(ctx context.Context, w io.Writer, errOut io.Writer, opts StatusOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "status", "--short"}, exec.RunOptions{})
		if err != nil {
			fmt.Fprintf(errOut, "=== %s (error) ===\n", name)
			fmt.Fprintf(errOut, "%s\n", err)
			continue
		}
		fmt.Fprintf(w, "=== %s ===\n", name)

		// Current branch (or detached HEAD) plus ahead/behind delta vs upstream.
		branch, detached, sha, ok := ws.branchInfo(ctx, repoDir)
		switch {
		case !ok:
			fmt.Fprintln(w, "branch: (unknown)")
		case detached:
			if sha != "" {
				fmt.Fprintf(w, "branch: (detached HEAD at %s)\n", sha)
			} else {
				fmt.Fprintln(w, "branch: (detached HEAD)")
			}
		default:
			fmt.Fprintf(w, "branch: %s (%s)\n", branch, ws.aheadBehind(ctx, repoDir))
		}

		// Working-tree status (porcelain) — unchanged behavior.
		if len(res.Stdout) == 0 {
			fmt.Fprintln(w, "(clean)")
		} else {
			_, _ = w.Write(res.Stdout)
		}

		// Other local branches besides the current one.
		if others := ws.otherBranches(ctx, repoDir, branch); others != "" {
			fmt.Fprintf(w, "other branches: %s\n", others)
		}

		// Worktrees registered for this repo.
		if wts := ws.worktreePaths(ctx, repoDir); wts != "" {
			fmt.Fprintf(w, "worktrees: %s\n", wts)
		}
	}
	return nil
}

// branchInfo returns the checked-out branch name for the repo at repoDir.
// When HEAD is detached it returns isDetached=true and the short commit sha
// (best-effort, may be empty). ok is false when the underlying git query fails,
// letting the caller degrade gracefully rather than abort. (Distinct from
// currentBranch in push.go, which returns an error and does not distinguish a
// detached HEAD from a branch literally named "HEAD".)
func (ws *Workspace) branchInfo(ctx context.Context, repoDir string) (name string, isDetached bool, sha string, ok bool) {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD"}, exec.RunOptions{})
	if err != nil {
		return "", false, "", false
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "HEAD" {
		// Detached HEAD: report the short commit sha when we can get it.
		shaRes, shaErr := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "--short", "HEAD"}, exec.RunOptions{})
		s := ""
		if shaErr == nil {
			s = strings.TrimSpace(string(shaRes.Stdout))
		}
		return "", true, s, true
	}
	return out, false, "", true
}

// aheadBehind returns the ahead/behind delta of the current branch versus its
// upstream/tracking branch, formatted as "ahead X, behind Y". When there is no
// upstream (or the query fails) it returns "no upstream" rather than erroring.
func (ws *Workspace) aheadBehind(ctx context.Context, repoDir string) string {
	// HEAD...@{upstream}: left side (HEAD) counts ahead, right side counts behind.
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}, exec.RunOptions{})
	if err != nil {
		return "no upstream"
	}
	fields := strings.Fields(string(res.Stdout))
	if len(fields) != 2 {
		return "no upstream"
	}
	return fmt.Sprintf("ahead %s, behind %s", fields[0], fields[1])
}

// otherBranches returns a comma-separated list of local branch names other than
// current. It returns "" when there are no other branches or the query fails.
func (ws *Workspace) otherBranches(ctx context.Context, repoDir, current string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	var others []string
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		b := strings.TrimSpace(line)
		if b == "" || b == current {
			continue
		}
		others = append(others, b)
	}
	return strings.Join(others, ", ")
}

// worktreePaths returns a comma-separated list of the worktree paths registered
// for the repo. It returns "" when the query fails. A successful query yields at
// least the main worktree, so any additional entries are the extra worktrees.
func (ws *Workspace) worktreePaths(ctx context.Context, repoDir string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "worktree", "list", "--porcelain"}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	var paths []string
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		if p, found := strings.CutPrefix(line, "worktree "); found {
			paths = append(paths, strings.TrimSpace(p))
		}
	}
	return strings.Join(paths, ", ")
}
