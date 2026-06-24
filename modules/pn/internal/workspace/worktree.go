package workspace

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// WorktreeAddOptions configures WorktreeAdd.
type WorktreeAddOptions struct {
	// Branch is the feature branch to check out (or create) in each repo.
	Branch string
	// CommitIsh is an optional start-point for the new branch. When empty,
	// git uses the canonical repo's current HEAD (exactly as git worktree add does).
	CommitIsh string
}

// WorktreeListOptions configures WorktreeList.
type WorktreeListOptions struct{}

// WorktreeRemoveOptions configures WorktreeRemove.
type WorktreeRemoveOptions struct {
	// Branch names the set to remove (the set lives at <worktrees_dir>/<branch>).
	Branch string
	// Force passes --force to git worktree remove, allowing removal of dirty/locked worktrees.
	Force bool
}

// WorktreePruneOptions configures WorktreePrune.
type WorktreePruneOptions struct{}

// WorktreeAdd creates a coordinated worktree set for Branch under w.WorktreesDir().
// It pre-flights all checks before creating anything, then runs git worktree add
// in each canonical repo (in deterministic order), and copies pn-workspace.toml,
// pn-workspace.lock.json, and pn-workspace.revs.json into the set directory.
func (w *Workspace) WorktreeAdd(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreeAddOptions) error {
	branch := opts.Branch
	setDir := filepath.Join(w.WorktreesDir(), branch)
	names := w.topoAlpha(ctx)

	// --- Pre-flight checks (all before creating anything) ---

	// 1. Every config repo must exist on disk in the canonical root.
	for _, repo := range names {
		canonical := filepath.Join(w.Root(), repo)
		if !isGitRepo(canonical) {
			return fmt.Errorf("worktree add: repo %q not found at %s (run `pn workspace clone` first)", repo, canonical)
		}
	}

	// 2. The set dir must not already exist.
	if dirExists(setDir) {
		return fmt.Errorf("worktree add: set directory already exists: %s", setDir)
	}

	// 3. For every repo, <branch> must NOT already be checked out in any worktree.
	for _, repo := range names {
		canonical := filepath.Join(w.Root(), repo)
		if err := w.assertBranchNotCheckedOut(ctx, canonical, repo, branch); err != nil {
			return err
		}
	}

	// --- Create the set directory ---
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		return fmt.Errorf("worktree add: create set dir %s: %w", setDir, err)
	}

	// --- git worktree add per repo ---
	for _, repo := range names {
		fmt.Fprintf(out, "  --== worktree add %s ==--  \n", repo)
		canonical := filepath.Join(w.Root(), repo)
		setRepo := filepath.Join(setDir, repo)

		// Check if <branch> already exists locally in this repo.
		branchExists := w.localBranchExists(ctx, canonical, branch)

		var gitArgs []string
		if branchExists {
			// Branch exists: check it out.
			gitArgs = []string{"-C", canonical, "worktree", "add", setRepo, branch}
		} else {
			// Branch does not exist: create it with -b.
			gitArgs = []string{"-C", canonical, "worktree", "add", "-b", branch, setRepo}
			if opts.CommitIsh != "" {
				gitArgs = append(gitArgs, opts.CommitIsh)
			}
		}

		if _, err := w.runner.Run(ctx, "git", gitArgs, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("worktree add: git worktree add in repo %q: %w", repo, err)
		}
	}

	// --- Copy config/lock/revs into the set dir ---
	if err := copyFile(filepath.Join(w.Root(), ConfigFileName), filepath.Join(setDir, ConfigFileName)); err != nil {
		return fmt.Errorf("worktree add: copy %s: %w", ConfigFileName, err)
	}
	if err := copyFile(filepath.Join(w.Root(), LockFileName), filepath.Join(setDir, LockFileName)); err != nil {
		return fmt.Errorf("worktree add: copy %s: %w", LockFileName, err)
	}
	// RevLock is optional.
	revsSrc := filepath.Join(w.Root(), RevLockFileName)
	if fileExists(revsSrc) {
		if err := copyFile(revsSrc, filepath.Join(setDir, RevLockFileName)); err != nil {
			return fmt.Errorf("worktree add: copy %s: %w", RevLockFileName, err)
		}
	}

	return nil
}

// WorktreeList lists the worktree sets under w.WorktreesDir(), one per line.
// If the worktrees directory does not exist, nothing is printed (not an error).
func (w *Workspace) WorktreeList(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreeListOptions) error {
	wtDir := w.WorktreesDir()
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("worktree list: read %s: %w", wtDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		setName := e.Name()
		// Dot-prefixed dirs (e.g. .pn-update, the ephemeral update-worktree area)
		// are not coordinated sets — skip them.
		if strings.HasPrefix(setName, ".") {
			continue
		}
		// The set dir name IS the branch by construction, so a second branch
		// column would just duplicate it — print the name once.
		fmt.Fprintln(out, setName)
	}
	return nil
}

// WorktreeRemove removes the coordinated worktree set for Branch.
// It mirrors git worktree remove: relies on git's dirty/locked refusal unless --force.
// Deletes the set directory after all git worktree removes succeed.
// Does NOT delete any branches.
func (w *Workspace) WorktreeRemove(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreeRemoveOptions) error {
	branch := opts.Branch
	setDir := filepath.Join(w.WorktreesDir(), branch)

	if !dirExists(setDir) {
		return fmt.Errorf("worktree remove: set directory does not exist: %s", setDir)
	}

	names := w.topoAlpha(ctx)

	for _, repo := range names {
		canonical := filepath.Join(w.Root(), repo)
		setRepo := filepath.Join(setDir, repo)

		// Only attempt removal for repos whose worktree actually exists.
		if !dirExists(setRepo) {
			continue
		}

		fmt.Fprintf(out, "  --== worktree remove %s ==--  \n", repo)
		gitArgs := []string{"-C", canonical, "worktree", "remove", setRepo}
		if opts.Force {
			gitArgs = append(gitArgs, "--force")
		}
		if _, err := w.runner.Run(ctx, "git", gitArgs, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("worktree remove: git worktree remove in repo %q: %w", repo, err)
		}
	}

	// Remove the now-empty set directory (still holds copied toml/lock/revs).
	if err := os.RemoveAll(setDir); err != nil {
		return fmt.Errorf("worktree remove: delete set dir %s: %w", setDir, err)
	}
	return nil
}

// WorktreePrune runs git worktree prune in every canonical repo, clearing
// stale .git/worktrees admin entries left when a set dir was deleted manually
// or a partial add failed.
func (w *Workspace) WorktreePrune(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreePruneOptions) error {
	names := w.topoAlpha(ctx)
	for _, repo := range names {
		fmt.Fprintf(out, "  --== worktree prune %s ==--  \n", repo)
		canonical := filepath.Join(w.Root(), repo)
		if _, err := w.runner.Run(ctx, "git",
			[]string{"-C", canonical, "worktree", "prune"},
			exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("worktree prune: git worktree prune in repo %q: %w", repo, err)
		}
	}
	return nil
}

// --- helpers ---

// assertBranchNotCheckedOut parses `git worktree list --porcelain` output for
// the canonical repo and returns an error if <branch> is already checked out.
func (w *Workspace) assertBranchNotCheckedOut(ctx context.Context, canonical, repo, branch string) error {
	res, err := w.runner.Run(ctx, "git",
		[]string{"-C", canonical, "worktree", "list", "--porcelain"},
		exec.RunOptions{})
	if err != nil {
		return fmt.Errorf("worktree add: git worktree list in repo %q: %w", repo, err)
	}
	target := "branch refs/heads/" + branch
	scanner := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for scanner.Scan() {
		if strings.TrimRight(scanner.Text(), "\r") == target {
			return fmt.Errorf("worktree add: branch %q is already checked out in a worktree of repo %q", branch, repo)
		}
	}
	return nil
}

// localBranchExists reports whether <branch> exists as a local branch in the repo.
func (w *Workspace) localBranchExists(ctx context.Context, canonical, branch string) bool {
	_, err := w.runner.Run(ctx, "git",
		[]string{"-C", canonical, "rev-parse", "--verify", "--quiet", "refs/heads/" + branch},
		exec.RunOptions{})
	return err == nil
}

// copyFile copies src to dst verbatim, creating dst if needed.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
