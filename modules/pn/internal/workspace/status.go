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
// Each per-repo block is laid out as:
//
//	<repo>
//	  <branch>  ↑A ↓B  (clean)          # primary worktree: branch + ahead/behind
//	     M path/to/changed              #   (porcelain lines when dirty, in place of "(clean)")
//	  worktrees:                        # omitted when there are no linked worktrees
//	    <dir> (<branch>)  ↑A ↓B         #   one per line; primary worktree is excluded
//	  branches:                         # omitted when every branch is checked out somewhere
//	    <branch>  ↑A ↓B                 #   local branches not checked out in any worktree
//
// Blocks are separated by a blank line. The primary line's ahead/behind is
// measured versus the branch's upstream (so unpushed/unpulled commits show);
// worktree and branch deltas are measured versus the repo's default branch (so
// feature divergence shows). Every git query degrades gracefully: a failing one
// is omitted (or reported as unknown) rather than aborting the loop.
//
// Status is a terminal-optional command: if no terminal is configured it emits
// a warning to errOut and continues.
func (ws *Workspace) Status(ctx context.Context, w io.Writer, errOut io.Writer, opts StatusOptions) error {
	if opts.Terminal == "" && ws.config.Workspace.Terminal == "" {
		fmt.Fprintln(errOut, terminalWarningMessage)
	}
	names := ws.topoAlpha(ctx)
	first := true
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "status", "--short"}, exec.RunOptions{})
		if err != nil {
			fmt.Fprintf(errOut, "%s (error)\n", name)
			fmt.Fprintf(errOut, "%s\n", err)
			continue
		}

		// Blank line between repo blocks (not before the first).
		if !first {
			fmt.Fprintln(w)
		}
		first = false

		fmt.Fprintf(w, "%s\n", name)

		// Default branch for this repo (config fills the default with "main").
		defBranch := ws.config.Repos[name].Branch
		if defBranch == "" {
			defBranch = "main"
		}

		// Primary worktree line: branch (or detached HEAD) + ahead/behind vs
		// upstream, then the working-tree state on the same line ("(clean)")
		// or the porcelain lines below it (dirty).
		branch, detached, sha, ok := ws.branchInfo(ctx, repoDir)
		var head string
		switch {
		case !ok:
			head = "(unknown branch)"
		case detached && sha != "":
			head = fmt.Sprintf("(detached HEAD at %s)", sha)
		case detached:
			head = "(detached HEAD)"
		default:
			head = branch
			if arrows, hasUpstream := ws.upstreamArrows(ctx, repoDir); hasUpstream {
				head += "  " + arrows
			} else {
				head += "  (no upstream)"
			}
		}
		if len(res.Stdout) == 0 {
			fmt.Fprintf(w, "  %s  (clean)\n", head)
		} else {
			fmt.Fprintf(w, "  %s\n", head)
			for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}

		// Worktrees registered for the repo. The first entry is the primary
		// worktree (this repo checkout) and is excluded; the rest are listed
		// one per line. checkedOut collects every branch checked out in any
		// worktree so it can be excluded from the branches section below.
		checkedOut := map[string]bool{}
		if !detached && branch != "" {
			checkedOut[branch] = true // the primary worktree's branch
		}
		wts, wtOK := ws.worktreeList(ctx, repoDir)
		var linked []worktreeInfo
		if wtOK {
			for i, wt := range wts {
				if wt.branch != "" {
					checkedOut[wt.branch] = true
				}
				if i > 0 {
					linked = append(linked, wt)
				}
			}
		}
		if len(linked) > 0 {
			fmt.Fprintln(w, "  worktrees:")
			for _, wt := range linked {
				label := filepath.Base(wt.path)
				var branchPart, ref string
				if wt.detached {
					branchPart = "detached " + short(wt.headSHA)
					ref = wt.headSHA
				} else {
					branchPart = wt.branch
					ref = "refs/heads/" + wt.branch
				}
				fmt.Fprintf(w, "    %s (%s)%s\n", label, branchPart,
					suffixArrows(ws.deltaArrows(ctx, repoDir, ref, "refs/heads/"+defBranch)))
			}
		}

		// Local branches not checked out in any worktree.
		var loose []string
		for _, b := range ws.localBranches(ctx, repoDir) {
			if !checkedOut[b] {
				loose = append(loose, b)
			}
		}
		if len(loose) > 0 {
			fmt.Fprintln(w, "  branches:")
			for _, b := range loose {
				fmt.Fprintf(w, "    %s%s\n", b,
					suffixArrows(ws.deltaArrows(ctx, repoDir, "refs/heads/"+b, "refs/heads/"+defBranch)))
			}
		}
	}
	return nil
}

// suffixArrows prefixes a non-empty ahead/behind string with the column gap
// used between a branch/worktree label and its delta. An empty delta (query
// failed) yields no suffix.
func suffixArrows(arrows string) string {
	if arrows == "" {
		return ""
	}
	return "  " + arrows
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

// aheadBehindCounts returns the ahead/behind commit counts of the current
// branch versus its upstream/tracking branch. ok is false when there is no
// upstream (or the query fails), letting callers phrase that case themselves.
func (ws *Workspace) aheadBehindCounts(ctx context.Context, repoDir string) (ahead, behind string, ok bool) {
	// HEAD...@{upstream}: left side (HEAD) counts ahead, right side counts behind.
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"}, exec.RunOptions{})
	if err != nil {
		return "", "", false
	}
	fields := strings.Fields(string(res.Stdout))
	if len(fields) != 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}

// aheadBehind returns the ahead/behind delta of the current branch versus its
// upstream/tracking branch, formatted as "ahead X, behind Y". When there is no
// upstream (or the query fails) it returns "no upstream" rather than erroring.
// (Used by doctor's branch-synced message; Status renders the same counts as
// compact arrows via upstreamArrows.)
func (ws *Workspace) aheadBehind(ctx context.Context, repoDir string) string {
	ahead, behind, ok := ws.aheadBehindCounts(ctx, repoDir)
	if !ok {
		return "no upstream"
	}
	return fmt.Sprintf("ahead %s, behind %s", ahead, behind)
}

// upstreamArrows renders the current branch's ahead/behind versus its upstream
// as "↑A ↓B". ok is false when there is no upstream.
func (ws *Workspace) upstreamArrows(ctx context.Context, repoDir string) (string, bool) {
	ahead, behind, ok := ws.aheadBehindCounts(ctx, repoDir)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("↑%s ↓%s", ahead, behind), true
}

// deltaArrows renders the ahead/behind of ref versus base as "↑A ↓B" (ahead =
// commits in ref not in base). It returns "" when either ref is unresolvable or
// the query fails, so the caller can omit the delta rather than abort.
func (ws *Workspace) deltaArrows(ctx context.Context, repoDir, ref, base string) string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-list", "--left-right", "--count", ref + "..." + base}, exec.RunOptions{})
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(res.Stdout))
	if len(fields) != 2 {
		return ""
	}
	return fmt.Sprintf("↑%s ↓%s", fields[0], fields[1])
}

// localBranches returns the local branch names for the repo, in git's default
// (alphabetical) order. It returns nil when the query fails.
func (ws *Workspace) localBranches(ctx context.Context, repoDir string) []string {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "branch", "--format=%(refname:short)"}, exec.RunOptions{})
	if err != nil {
		return nil
	}
	var branches []string
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	return branches
}

// worktreeInfo is one entry from `git worktree list --porcelain`.
type worktreeInfo struct {
	path     string
	branch   string // short branch name; "" when detached
	headSHA  string // full commit sha at the worktree HEAD
	detached bool
}

// worktreeList parses `git worktree list --porcelain` into per-worktree
// entries, in git's listing order (the primary worktree first). ok is false
// when the query fails, letting the caller omit the worktrees section.
func (ws *Workspace) worktreeList(ctx context.Context, repoDir string) ([]worktreeInfo, bool) {
	res, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "worktree", "list", "--porcelain"}, exec.RunOptions{})
	if err != nil {
		return nil, false
	}
	var list []worktreeInfo
	var cur *worktreeInfo
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &worktreeInfo{path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case cur == nil:
			// Attribute lines before any "worktree " header: ignore.
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.headSHA = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			cur.branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		case line == "detached":
			cur.detached = true
		}
	}
	flush()
	return list, true
}
