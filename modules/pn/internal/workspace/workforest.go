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

// WorkforestAddOptions configures WorkforestAdd.
type WorkforestAddOptions struct {
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

// WorkforestAddRepoOptions configures WorkforestAddRepo.
type WorkforestAddRepoOptions struct {
	// Branch names the existing set (the set lives at <workforests_dir>/<branch>).
	Branch string
	// Repo is the workspace repo key to add to the set.
	Repo string
}

// WorkforestRemoveRepoOptions configures WorkforestRemoveRepo.
type WorkforestRemoveRepoOptions struct {
	// Branch names the existing set.
	Branch string
	// Repo is the workspace repo key to remove from the set.
	Repo string
	// Force passes --force to git worktree remove (dirty/locked worktrees).
	Force bool
}

// WorkforestListOptions configures WorkforestList.
type WorkforestListOptions struct{}

// WorkforestRemoveOptions configures WorkforestRemove.
type WorkforestRemoveOptions struct {
	// Branch names the set to remove (the set lives at <workforests_dir>/<branch>).
	Branch string
	// Force passes --force to git worktree remove, allowing removal of dirty/locked worktrees.
	Force bool
}

// WorkforestPruneOptions configures WorkforestPrune.
type WorkforestPruneOptions struct{}

// WorkforestAdd creates a coordinated workforest set for Branch under w.WorkforestsDir().
// It pre-flights all checks before creating anything, then runs git worktree add
// in each canonical repo (in deterministic order), and copies pn-workspace.toml
// and pn-workspace.lock.json into the set directory.
func (w *Workspace) WorkforestAdd(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestAddOptions) error {
	branch := opts.Branch
	setDir := filepath.Join(w.WorkforestsDir(), branch)

	// Resolve the member repos (subset or all), validated + topo-ordered.
	names, err := w.memberRepos(ctx, opts.Repos)
	if err != nil {
		return fmt.Errorf("workforest add: %w", err)
	}

	// --- Pre-flight checks (all before creating anything) ---

	// 1. Every member repo must exist on disk in the canonical root.
	for _, repo := range names {
		canonical := filepath.Join(w.Root(), repo)
		if !isGitRepo(canonical) {
			return fmt.Errorf("workforest add: repo %q not found at %s (run `pn workspace clone` first)", repo, canonical)
		}
	}

	// 2. The set dir must not already exist.
	if dirExists(setDir) {
		return fmt.Errorf("workforest add: set directory already exists: %s", setDir)
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
		return fmt.Errorf("workforest add: create set dir %s: %w", setDir, err)
	}

	// --- git worktree add per member repo ---
	for _, repo := range names {
		if err := w.gitWorktreeAddOne(ctx, out, setDir, repo, branch, opts.CommitIsh); err != nil {
			return err
		}
		// (Re)install the repo's opt-in git pre-commit hooks IN its freshly-created
		// worktree so the set gets working, branch-correct hooks. Best-effort: a
		// failure here is warned and skipped, never fatal — the worktree is already
		// created and rolling it back would surprise the user (mirrors the
		// post-hook warn-only philosophy in cli/hooks.go). We read the opt-in
		// output list from the canonical config (w.config); the set's own
		// pn-workspace.toml is not written until writeSetMembership below.
		//
		// Shared-hooks caveat: git worktrees share the canonical repo's
		// .git/hooks (hooks resolve via --git-common-dir), but the installed hook
		// runs prek against a RELATIVE .pre-commit-config.yaml, which each worktree
		// resolves to its OWN symlink — so the pre-commit CONFIG is
		// per-worktree-isolated (a config/formatter change tested in a set does not
		// touch canonical). The one shared element is the prek BINARY version baked
		// into the shared hook script: if the set's branch bumps prek itself,
		// re-installing from the set rewrites the shared hook's prek path (affects
		// canonical). Config/formatter changes do NOT cross-contaminate.
	}

	// --- Write the set's config/lock (filtered to the member set) ---
	if err := w.writeSetMembership(out, errOut, setDir, names); err != nil {
		return err
	}

	// Fire post-clone hooks in each new worktree (set-rooted, so {nix_run}
	// overrides resolve to the set's worktrees — P1-safe). Warn-only.
	installSetHooks(ctx, w, setDir, names, out, errOut)

	return nil
}

// installSetHooks fires the post-clone event for the given repos against a
// Workspace rooted at setDir, so each new worktree's per-repo {nix_run} hooks
// (re)install its gate with overrides pinned to the set's worktrees. Failures
// are warn-only and never abort the workforest operation.
func installSetHooks(ctx context.Context, w *Workspace, setDir string, repos []string, out, errOut io.Writer) {
	setWs, err := Open(setDir, w.runner)
	if err != nil {
		fmt.Fprintf(errOut, "warning: workforest hooks: open set %s: %v\n", setDir, err)
		return
	}
	defer setWs.Close()
	if err := setWs.RunEventHooks(ctx, HookPhasePost, "clone", repos, out, errOut); err != nil {
		fmt.Fprintf(errOut, "warning: workforest hooks: %v\n", err)
	}
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
		return fmt.Errorf("workforest add: git worktree add in repo %q: %w", repo, err)
	}
	return nil
}

// writeSetMembership writes the set's pn-workspace.toml and .lock.json,
// restricted to the given member repos. When members covers every config repo
// the files are byte-identical to the canonical ones; for a strict subset the
// config/lock are filtered and a notice is written to errOut naming any
// consumer→dep edges that fall back to canonical because the dependency is
// excluded from the set.
func (w *Workspace) writeSetMembership(out io.Writer, errOut io.Writer, setDir string, names []string) error {
	memberSet := make(map[string]bool, len(names))
	for _, n := range names {
		memberSet[n] = true
	}
	isFullSet := len(names) == len(w.config.Repos)

	// Config: copy verbatim for a full set, else write the filtered subset.
	if isFullSet {
		if err := copyFile(filepath.Join(w.Root(), ConfigFileName), filepath.Join(setDir, ConfigFileName)); err != nil {
			return fmt.Errorf("workforest add: copy %s: %w", ConfigFileName, err)
		}
	} else {
		subCfg := filterConfig(w.config, memberSet)
		if err := writeConfigTOMLTo(filepath.Join(setDir, ConfigFileName), subCfg); err != nil {
			return fmt.Errorf("workforest add: write filtered %s: %w", ConfigFileName, err)
		}
	}

	// Lock: copy verbatim for a full set, else write the filtered subset.
	if isFullSet {
		if err := copyFile(filepath.Join(w.Root(), LockFileName), filepath.Join(setDir, LockFileName)); err != nil {
			return fmt.Errorf("workforest add: copy %s: %w", LockFileName, err)
		}
	} else {
		subLock := filterLock(w.lock, memberSet)
		if err := WriteLock(filepath.Join(setDir, LockFileName), subLock); err != nil {
			return fmt.Errorf("workforest add: write filtered %s: %w", LockFileName, err)
		}
		w.noticeExcludedDeps(errOut, memberSet)
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
			"pn: workforest set excludes %q, a workspace dependency of %q (via input %q); it will resolve against its locked flake input, not a set-internal override\n",
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

// WorkforestAddRepo adds a single workspace repo to an existing set. It mirrors
// `git worktree add`: pre-flights (set exists, repo is a known workspace repo
// in the canonical root, repo is not already a member, branch not checked out
// elsewhere), runs `git worktree add` for the one repo on the set's branch, then
// rewrites the set's filtered config/lock to include the new member.
func (w *Workspace) WorkforestAddRepo(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestAddRepoOptions) error {
	branch := opts.Branch
	repo := opts.Repo
	setDir := filepath.Join(w.WorkforestsDir(), branch)

	// Pre-flight: set must exist.
	if !dirExists(setDir) {
		return fmt.Errorf("workforest add-repo: set directory does not exist: %s (create it with `pn workspace workforest add %s`)", setDir, branch)
	}
	// Pre-flight: repo must be a declared workspace repo.
	if _, ok := w.config.Repos[repo]; !ok {
		return fmt.Errorf("workforest add-repo: unknown repo %q (not declared in %s)", repo, ConfigFileName)
	}
	// Pre-flight: repo must exist on disk in the canonical root.
	canonical := filepath.Join(w.Root(), repo)
	if !isGitRepo(canonical) {
		return fmt.Errorf("workforest add-repo: repo %q not found at %s (run `pn workspace clone` first)", repo, canonical)
	}
	// Pre-flight: repo must not already be a member of the set.
	members, err := w.readSetMembers(setDir)
	if err != nil {
		return fmt.Errorf("workforest add-repo: %w", err)
	}
	if members[repo] {
		return fmt.Errorf("workforest add-repo: repo %q is already a member of set %q", repo, branch)
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

	// (Re)install the repo's opt-in git pre-commit hooks IN the new worktree.
	// Best-effort: a failure is warned and skipped, never fatal — the worktree
	// is already created (mirrors the post-hook warn-only philosophy in
	// cli/hooks.go). Opt-in output list comes from the canonical config
	// (w.config). See WorkforestAdd for the shared-hooks caveat: worktrees share
	// .git/hooks, but each resolves its own relative .pre-commit-config.yaml
	// symlink, so pre-commit config/formatter changes stay per-worktree-isolated;
	// only a prek-binary version bump baked into the shared hook script crosses
	// back to canonical.
	// Recompute membership and rewrite the set's filtered config/lock.
	members[repo] = true
	if err := w.rewriteSetMembership(errOut, setDir, members); err != nil {
		return err
	}

	// Fire post-clone hooks in the newly-added worktree (set-rooted). Warn-only.
	installSetHooks(ctx, w, setDir, []string{repo}, out, errOut)

	return nil
}

// WorkforestRemoveRepo removes a single workspace repo from an existing set. It
// mirrors `git worktree remove`: pre-flights (set exists, repo is a member,
// it is not the last member), runs `git worktree remove` (refusing dirty/locked
// unless Force), then rewrites the set's filtered config/lock to drop the
// member. Does NOT delete the branch.
func (w *Workspace) WorkforestRemoveRepo(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestRemoveRepoOptions) error {
	branch := opts.Branch
	repo := opts.Repo
	setDir := filepath.Join(w.WorkforestsDir(), branch)

	// Pre-flight: set must exist.
	if !dirExists(setDir) {
		return fmt.Errorf("workforest remove-repo: set directory does not exist: %s", setDir)
	}
	// Pre-flight: repo must be a member.
	members, err := w.readSetMembers(setDir)
	if err != nil {
		return fmt.Errorf("workforest remove-repo: %w", err)
	}
	if !members[repo] {
		return fmt.Errorf("workforest remove-repo: repo %q is not a member of set %q", repo, branch)
	}
	// Pre-flight: refuse removing the last repo (would leave an empty,
	// inconsistent set — the user should `workforest remove %s` instead).
	if len(members) == 1 {
		return fmt.Errorf("workforest remove-repo: refusing to remove the last repo %q from set %q (use `pn workspace workforest remove %s` to delete the whole set)", repo, branch, branch)
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
			return fmt.Errorf("workforest remove-repo: git worktree remove in repo %q: %w", repo, err)
		}
	}

	// Recompute membership and rewrite the set's filtered config/lock.
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

// rewriteSetMembership rewrites the set's pn-workspace.toml / .lock.json
// filtered to memberSet, derived from the CANONICAL config/lock (w is rooted at
// canonical). Emits the excluded-dep notice to errOut. Used by add-repo /
// remove-repo, which always produce a subset of the canonical config.
func (w *Workspace) rewriteSetMembership(errOut io.Writer, setDir string, memberSet map[string]bool) error {
	if err := writeConfigTOMLTo(filepath.Join(setDir, ConfigFileName), filterConfig(w.config, memberSet)); err != nil {
		return fmt.Errorf("write set %s: %w", ConfigFileName, err)
	}
	if err := WriteLock(filepath.Join(setDir, LockFileName), filterLock(w.lock, memberSet)); err != nil {
		return fmt.Errorf("write set %s: %w", LockFileName, err)
	}
	w.noticeExcludedDeps(errOut, memberSet)
	return nil
}

// WorkforestList lists the workforest sets under w.WorkforestsDir(), one per line.
// If the workforests directory does not exist, nothing is printed (not an error).
func (w *Workspace) WorkforestList(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestListOptions) error {
	wtDir := w.WorkforestsDir()
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("workforest list: read %s: %w", wtDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		setName := e.Name()
		// Dot-prefixed dirs (e.g. .pn-update, the ephemeral update-worktree area)
		// are not coordinated workforest sets — skip them.
		if strings.HasPrefix(setName, ".") {
			continue
		}
		// The set dir name IS the branch by construction, so a second branch
		// column would just duplicate it — print the name once.
		fmt.Fprintln(out, setName)
	}
	return nil
}

// WorkforestRemove removes the coordinated workforest set for Branch.
// It mirrors git worktree remove: relies on git's dirty/locked refusal unless --force.
// Deletes the set directory after all git worktree removes succeed.
// Does NOT delete any branches.
func (w *Workspace) WorkforestRemove(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestRemoveOptions) error {
	branch := opts.Branch
	setDir := filepath.Join(w.WorkforestsDir(), branch)

	if !dirExists(setDir) {
		return fmt.Errorf("workforest remove: set directory does not exist: %s", setDir)
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
			return fmt.Errorf("workforest remove: git worktree remove in repo %q: %w", repo, err)
		}
	}

	// Remove the now-empty set directory (still holds copied toml/lock).
	if err := os.RemoveAll(setDir); err != nil {
		return fmt.Errorf("workforest remove: delete set dir %s: %w", setDir, err)
	}
	return nil
}

// WorkforestPrune runs git worktree prune in every canonical repo, clearing
// stale .git/worktrees admin entries left when a set dir was deleted manually
// or a partial add failed.
func (w *Workspace) WorkforestPrune(ctx context.Context, out io.Writer, errOut io.Writer, opts WorkforestPruneOptions) error {
	names := w.topoAlpha(ctx)
	for _, repo := range names {
		fmt.Fprintf(out, "  --== worktree prune %s ==--  \n", repo)
		canonical := filepath.Join(w.Root(), repo)
		if _, err := w.runner.Run(ctx, "git",
			[]string{"-C", canonical, "worktree", "prune"},
			exec.RunOptions{Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("workforest prune: git worktree prune in repo %q: %w", repo, err)
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
		return fmt.Errorf("workforest add: git worktree list in repo %q: %w", repo, err)
	}
	target := "branch refs/heads/" + branch
	scanner := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for scanner.Scan() {
		if strings.TrimRight(scanner.Text(), "\r") == target {
			return fmt.Errorf("workforest add: branch %q is already checked out in a worktree of repo %q", branch, repo)
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
