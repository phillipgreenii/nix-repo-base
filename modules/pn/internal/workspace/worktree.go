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
	// Repos optionally restricts the set to a subset of the workspace repos.
	// When empty, the set contains every repo in pn-workspace.toml (the default).
	// Each entry must be a configured repo key; unknown keys are an error.
	Repos []string
}

// WorktreeAddRepoOptions configures WorktreeAddRepo.
type WorktreeAddRepoOptions struct {
	// Branch names the existing set (the set lives at <worktrees_dir>/<branch>).
	Branch string
	// Repo is the workspace repo key to add to the set.
	Repo string
}

// WorktreeRemoveRepoOptions configures WorktreeRemoveRepo.
type WorktreeRemoveRepoOptions struct {
	// Branch names the existing set.
	Branch string
	// Repo is the workspace repo key to remove from the set.
	Repo string
	// Force passes --force to git worktree remove (dirty/locked worktrees).
	Force bool
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

	// Resolve the member repos (subset or all), validated + topo-ordered.
	names, err := w.memberRepos(ctx, opts.Repos)
	if err != nil {
		return fmt.Errorf("worktree add: %w", err)
	}

	// --- Pre-flight checks (all before creating anything) ---

	// 1. Every member repo must exist on disk in the canonical root.
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

	// 3. For every member repo, <branch> must NOT already be checked out in any worktree.
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

	// --- git worktree add per member repo ---
	for _, repo := range names {
		if err := w.gitWorktreeAddOne(ctx, out, setDir, repo, branch, opts.CommitIsh); err != nil {
			return err
		}
	}

	// --- Write the set's config/lock/revs (filtered to the member set) ---
	if err := w.writeSetMembership(out, errOut, setDir, names); err != nil {
		return err
	}

	return nil
}

// gitWorktreeAddOne runs `git worktree add` for one repo into the set dir,
// mirroring git worktree add semantics: check out <branch> if it exists locally,
// otherwise create it with -b from the optional commit-ish (default: HEAD).
func (w *Workspace) gitWorktreeAddOne(ctx context.Context, out io.Writer, setDir, repo, branch, commitIsh string) error {
	fmt.Fprintf(out, "  --== worktree add %s ==--  \n", repo)
	canonical := filepath.Join(w.Root(), repo)
	setRepo := filepath.Join(setDir, repo)

	branchExists := w.localBranchExists(ctx, canonical, branch)

	var gitArgs []string
	if branchExists {
		gitArgs = []string{"-C", canonical, "worktree", "add", setRepo, branch}
	} else {
		gitArgs = []string{"-C", canonical, "worktree", "add", "-b", branch, setRepo}
		if commitIsh != "" {
			gitArgs = append(gitArgs, commitIsh)
		}
	}

	if _, err := w.runner.Run(ctx, "git", gitArgs, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
		return fmt.Errorf("worktree add: git worktree add in repo %q: %w", repo, err)
	}
	return nil
}

// writeSetMembership writes the set's pn-workspace.toml, .lock.json, and (if
// present) .revs.json, restricted to the given member repos. When members
// covers every config repo the files are byte-identical to the canonical ones;
// for a strict subset the config/lock/revs are filtered and a notice is written
// to errOut naming any consumer→dep edges that fall back to canonical because
// the dependency is excluded from the set.
func (w *Workspace) writeSetMembership(out io.Writer, errOut io.Writer, setDir string, names []string) error {
	memberSet := make(map[string]bool, len(names))
	for _, n := range names {
		memberSet[n] = true
	}
	isFullSet := len(names) == len(w.config.Repos)

	// Config: copy verbatim for a full set, else write the filtered subset.
	if isFullSet {
		if err := copyFile(filepath.Join(w.Root(), ConfigFileName), filepath.Join(setDir, ConfigFileName)); err != nil {
			return fmt.Errorf("worktree add: copy %s: %w", ConfigFileName, err)
		}
	} else {
		subCfg := filterConfig(w.config, memberSet)
		if err := writeConfigTOMLTo(filepath.Join(setDir, ConfigFileName), subCfg); err != nil {
			return fmt.Errorf("worktree add: write filtered %s: %w", ConfigFileName, err)
		}
	}

	// Lock: copy verbatim for a full set, else write the filtered subset.
	if isFullSet {
		if err := copyFile(filepath.Join(w.Root(), LockFileName), filepath.Join(setDir, LockFileName)); err != nil {
			return fmt.Errorf("worktree add: copy %s: %w", LockFileName, err)
		}
	} else {
		subLock := filterLock(w.lock, memberSet)
		if err := WriteLock(filepath.Join(setDir, LockFileName), subLock); err != nil {
			return fmt.Errorf("worktree add: write filtered %s: %w", LockFileName, err)
		}
		w.noticeExcludedDeps(errOut, memberSet)
	}

	// RevLock is optional; copy verbatim for a full set, else filter.
	revsSrc := filepath.Join(w.Root(), RevLockFileName)
	if fileExists(revsSrc) {
		if isFullSet {
			if err := copyFile(revsSrc, filepath.Join(setDir, RevLockFileName)); err != nil {
				return fmt.Errorf("worktree add: copy %s: %w", RevLockFileName, err)
			}
		} else {
			subRevs := filterRevLock(w.revLock, memberSet)
			if err := WriteRevLock(filepath.Join(setDir, RevLockFileName), subRevs); err != nil {
				return fmt.Errorf("worktree add: write filtered %s: %w", RevLockFileName, err)
			}
		}
	}
	return nil
}

// noticeExcludedDeps writes a notice to errOut for every workspace dependency
// edge whose consumer is a member but whose target is excluded from the set.
// Such an input resolves against the consumer's own locked flake input
// (canonical) rather than a set-internal override — deterministic, but worth
// flagging so the agent is not surprised that the excluded dep is not live.
func (w *Workspace) noticeExcludedDeps(errOut io.Writer, memberSet map[string]bool) {
	if w.lock == nil || errOut == nil {
		return
	}
	for _, e := range excludedDepEdges(w.lock, memberSet) {
		fmt.Fprintf(errOut,
			"pn: worktree set excludes %q, a workspace dependency of %q (via input %q); it will resolve against its locked flake input, not a set-internal override\n",
			e.Target, e.Consumer, e.Alias)
	}
}

// memberRepos resolves the set members. When requested is empty, returns every
// config repo in topological order. Otherwise validates each requested key
// against the config (erroring on unknown keys) and returns the subset in
// topological order.
func (w *Workspace) memberRepos(ctx context.Context, requested []string) ([]string, error) {
	all := w.topoAlpha(ctx)
	if len(requested) == 0 {
		return all, nil
	}
	want := make(map[string]bool, len(requested))
	var unknown []string
	for _, r := range requested {
		if _, ok := w.config.Repos[r]; !ok {
			unknown = append(unknown, r)
			continue
		}
		want[r] = true
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown repo(s): %s (not declared in %s)", strings.Join(unknown, ", "), ConfigFileName)
	}
	out := make([]string, 0, len(want))
	for _, r := range all {
		if want[r] {
			out = append(out, r)
		}
	}
	return out, nil
}

// WorktreeAddRepo adds a single workspace repo to an existing set. It mirrors
// `git worktree add`: pre-flights (set exists, repo is a known workspace repo
// in the canonical root, repo is not already a member, branch not checked out
// elsewhere), runs `git worktree add` for the one repo on the set's branch, then
// rewrites the set's filtered config/lock/revs to include the new member.
func (w *Workspace) WorktreeAddRepo(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreeAddRepoOptions) error {
	branch := opts.Branch
	repo := opts.Repo
	setDir := filepath.Join(w.WorktreesDir(), branch)

	// Pre-flight: set must exist.
	if !dirExists(setDir) {
		return fmt.Errorf("worktree add-repo: set directory does not exist: %s (create it with `pn workspace worktree add %s`)", setDir, branch)
	}
	// Pre-flight: repo must be a declared workspace repo.
	if _, ok := w.config.Repos[repo]; !ok {
		return fmt.Errorf("worktree add-repo: unknown repo %q (not declared in %s)", repo, ConfigFileName)
	}
	// Pre-flight: repo must exist on disk in the canonical root.
	canonical := filepath.Join(w.Root(), repo)
	if !isGitRepo(canonical) {
		return fmt.Errorf("worktree add-repo: repo %q not found at %s (run `pn workspace clone` first)", repo, canonical)
	}
	// Pre-flight: repo must not already be a member of the set.
	members, err := w.readSetMembers(setDir)
	if err != nil {
		return fmt.Errorf("worktree add-repo: %w", err)
	}
	if members[repo] {
		return fmt.Errorf("worktree add-repo: repo %q is already a member of set %q", repo, branch)
	}
	// Pre-flight: branch must not be checked out in another worktree of this repo
	// (the set's own existing worktrees do not include this repo yet).
	if err := w.assertBranchNotCheckedOut(ctx, canonical, repo, branch); err != nil {
		return err
	}

	// Add the worktree for the one repo on the set's branch.
	if err := w.gitWorktreeAddOne(ctx, out, setDir, repo, branch, ""); err != nil {
		return err
	}

	// Recompute membership and rewrite the set's filtered config/lock/revs.
	members[repo] = true
	if err := w.rewriteSetMembership(errOut, setDir, members); err != nil {
		return err
	}
	return nil
}

// WorktreeRemoveRepo removes a single workspace repo from an existing set. It
// mirrors `git worktree remove`: pre-flights (set exists, repo is a member,
// it is not the last member), runs `git worktree remove` (refusing dirty/locked
// unless Force), then rewrites the set's filtered config/lock/revs to drop the
// member. Does NOT delete the branch.
func (w *Workspace) WorktreeRemoveRepo(ctx context.Context, out io.Writer, errOut io.Writer, opts WorktreeRemoveRepoOptions) error {
	branch := opts.Branch
	repo := opts.Repo
	setDir := filepath.Join(w.WorktreesDir(), branch)

	// Pre-flight: set must exist.
	if !dirExists(setDir) {
		return fmt.Errorf("worktree remove-repo: set directory does not exist: %s", setDir)
	}
	// Pre-flight: repo must be a member.
	members, err := w.readSetMembers(setDir)
	if err != nil {
		return fmt.Errorf("worktree remove-repo: %w", err)
	}
	if !members[repo] {
		return fmt.Errorf("worktree remove-repo: repo %q is not a member of set %q", repo, branch)
	}
	// Pre-flight: refuse removing the last repo (would leave an empty,
	// inconsistent set — the user should `worktree remove %s` instead).
	if len(members) == 1 {
		return fmt.Errorf("worktree remove-repo: refusing to remove the last repo %q from set %q (use `pn workspace worktree remove %s` to delete the whole set)", repo, branch, branch)
	}

	// Remove the worktree for the one repo (mirror force semantics).
	canonical := filepath.Join(w.Root(), repo)
	setRepo := filepath.Join(setDir, repo)
	if dirExists(setRepo) {
		fmt.Fprintf(out, "  --== worktree remove %s ==--  \n", repo)
		gitArgs := []string{"-C", canonical, "worktree", "remove", setRepo}
		if opts.Force {
			gitArgs = append(gitArgs, "--force")
		}
		if _, err := w.runner.Run(ctx, "git", gitArgs, exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("worktree remove-repo: git worktree remove in repo %q: %w", repo, err)
		}
	}

	// Recompute membership and rewrite the set's filtered config/lock/revs.
	delete(members, repo)
	if err := w.rewriteSetMembership(errOut, setDir, members); err != nil {
		return err
	}
	return nil
}

// readSetMembers reads the member repo keys from a set's pn-workspace.toml.
func (w *Workspace) readSetMembers(setDir string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(setDir, ConfigFileName))
	if err != nil {
		return nil, fmt.Errorf("read set %s: %w", ConfigFileName, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parse set %s: %w", ConfigFileName, err)
	}
	members := make(map[string]bool, len(cfg.Repos))
	for k := range cfg.Repos {
		members[k] = true
	}
	return members, nil
}

// rewriteSetMembership rewrites the set's pn-workspace.toml / .lock.json /
// .revs.json filtered to memberSet, derived from the CANONICAL config/lock/revs
// (w is rooted at canonical). Emits the excluded-dep notice to errOut. Used by
// add-repo / remove-repo, which always produce a subset of the canonical config.
func (w *Workspace) rewriteSetMembership(errOut io.Writer, setDir string, memberSet map[string]bool) error {
	if err := writeConfigTOMLTo(filepath.Join(setDir, ConfigFileName), filterConfig(w.config, memberSet)); err != nil {
		return fmt.Errorf("write set %s: %w", ConfigFileName, err)
	}
	if err := WriteLock(filepath.Join(setDir, LockFileName), filterLock(w.lock, memberSet)); err != nil {
		return fmt.Errorf("write set %s: %w", LockFileName, err)
	}
	if fileExists(filepath.Join(setDir, RevLockFileName)) || fileExists(filepath.Join(w.Root(), RevLockFileName)) {
		if err := WriteRevLock(filepath.Join(setDir, RevLockFileName), filterRevLock(w.revLock, memberSet)); err != nil {
			return fmt.Errorf("write set %s: %w", RevLockFileName, err)
		}
	}
	w.noticeExcludedDeps(errOut, memberSet)
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
