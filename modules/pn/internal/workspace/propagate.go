package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// workspaceAliasesFromLock returns the flake-input aliases that `consumer`
// declares against other workspace repos, read from the provided lock's edges.
// Unlike workspaceInputNamesFromEdges (which reads ws.lock, frequently empty on
// a fresh/stale checkout), the caller passes a lock derived via effectiveLock so
// propagation is not silently skipped when pn-workspace.lock.json is absent.
func workspaceAliasesFromLock(lock *Lock, consumer string) []string {
	if lock == nil {
		return nil
	}
	var names []string
	for _, e := range lock.Edges {
		if e.Consumer == consumer {
			names = append(names, e.Alias)
		}
	}
	sort.Strings(names)
	return names
}

// flakeLockNodes models just the parts of a Nix flake.lock that propagation
// needs: each node's declared inputs (to walk root → alias → node key) and its
// locked rev. Root names the root node key (defaults to "root").
type flakeLockNodes struct {
	Nodes map[string]struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
		Locked struct {
			Rev string `json:"rev"`
		} `json:"locked"`
	} `json:"nodes"`
	Root string `json:"root"`
}

// readAliasRevs maps each alias to its currently locked rev in the flake.lock at
// lockPath. Resolution mirrors checkFollows/tree.go: root.inputs[alias] yields a
// node key (a string) which is looked up in nodes. An alias whose root input is
// a follows *array* (not a string) has no independent lock node and is skipped
// without error (H2). A missing flake.lock yields an empty map, not an error.
func readAliasRevs(lockPath string, aliases []string) (map[string]string, error) {
	data, err := os.ReadFile(lockPath)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", lockPath, err)
	}
	var fl flakeLockNodes
	if err := json.Unmarshal(data, &fl); err != nil {
		return nil, fmt.Errorf("parse %s: %w", lockPath, err)
	}
	rootKey := fl.Root
	if rootKey == "" {
		rootKey = "root"
	}
	root, ok := fl.Nodes[rootKey]
	if !ok {
		return map[string]string{}, nil
	}
	revs := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		raw, ok := root.Inputs[alias]
		if !ok {
			continue
		}
		nodeKey, ok := asString(raw) // follows-array inputs return false → skip
		if !ok {
			continue
		}
		if node, ok := fl.Nodes[nodeKey]; ok && node.Locked.Rev != "" {
			revs[alias] = node.Locked.Rev
		}
	}
	return revs, nil
}

// shortRev truncates a git rev to the 7-char width used by the existing
// hand-authored "chore(deps): bump" commits.
func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// propagateWorkspaceEdges relocks `dir`'s workspace-sibling flake inputs to
// their upstreams' current revs and commits the change, ungated (independent of
// update-locks.sh's 12h cooldown). It is the automation of the manual
// "chore(deps): bump <alias> <old> -> <new>" commits.
//
//   - dir is the repo (or ephemeral worktree) root; git runs with -C dir.
//   - flakeRel is resolveFlakePath(name): the path to the flake.nix FILE
//     (e.g. "flake.nix" normally, "nix/flake.nix" for homelab). The function
//     derives its parent directory, where flake.lock lives and `nix flake
//     update` runs.
//   - aliases are the workspace-input names from workspaceAliasesFromLock.
//
// Critical invariants:
//   - --refresh bypasses Nix's tarball-ttl fetcher cache, without which an
//     upstream pushed seconds earlier in the same run resolves to its stale
//     cached rev and propagation no-ops on the exact run it must handle (C1).
//   - The working tree is left CLEAN whether or not a bump occurred: a
//     `nix flake update` that rewrites only lastModified (no rev change) is
//     reverted via `git checkout --`, so the subsequent rebase steps and
//     update-locks.sh's clean-tree gate are not tripped (C2).
//
// Returns (relocked, err). relocked is true only when a workspace-sibling rev
// actually moved and was committed; it is false for every no-op path (no
// aliases, no flake.lock, nix wrote nothing, or only lastModified churn). A
// non-nil error means the caller should mark the repo failed (no partial/dirty
// state is left behind); relocked is false alongside any error.
func (ws *Workspace) propagateWorkspaceEdges(ctx context.Context, out io.Writer, name, dir, flakeRel string, aliases []string) (bool, error) {
	if len(aliases) == 0 {
		return false, nil
	}
	// flakeRel is the flake.nix FILE path ("flake.nix", "nix/flake.nix"); the
	// dir we cd into and locate flake.lock in is its parent. filepath.Dir maps
	// "flake.nix" -> ".", "nix/flake.nix" -> "nix", and "" -> "." (so the empty
	// case is handled here too).
	flakeDirRel := filepath.Dir(flakeRel)
	flakeDir := filepath.Join(dir, flakeDirRel)
	lockRel := filepath.Join(flakeDirRel, "flake.lock")
	lockPath := filepath.Join(dir, lockRel)

	// No flake.lock yet (repo never locked) → nothing to propagate.
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		return false, nil
	}

	before, err := readAliasRevs(lockPath, aliases)
	if err != nil {
		return false, err
	}

	// Relock just the workspace-sibling inputs. --refresh is mandatory (C1).
	args := append([]string{"flake", "update", "--refresh"}, aliases...)
	if _, err := ws.runner.Run(ctx, "nix", args, exec.RunOptions{Dir: flakeDir, Stdout: out, Stderr: out}); err != nil {
		return false, fmt.Errorf("nix flake update %v: %w", aliases, err)
	}

	// nix wrote nothing to flake.lock → already clean, nothing to do.
	if ws.pathClean(ctx, dir, lockRel) {
		return false, nil
	}

	// flake.lock differs; determine whether any *rev* actually changed.
	after, err := readAliasRevs(lockPath, aliases)
	if err != nil {
		return false, err
	}
	var changed []string
	for _, alias := range aliases {
		if before[alias] != after[alias] {
			changed = append(changed, alias)
		}
	}
	if len(changed) == 0 {
		// Only lastModified/formatting churn — restore to keep the tree clean (C2).
		if _, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "checkout", "--", lockRel}, exec.RunOptions{}); err != nil {
			return false, fmt.Errorf("restore unchanged %s: %w", lockRel, err)
		}
		return false, nil
	}

	if _, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "add", lockRel}, exec.RunOptions{}); err != nil {
		return false, fmt.Errorf("git add %s: %w", lockRel, err)
	}
	msg := bumpCommitMessage(changed, before, after)
	// PREK_ALLOW_NO_CONFIG lets the commit succeed when the repo's prek pre-commit
	// hook (installed in the CANONICAL gitdir and SHARED into this ephemeral
	// worktree) fires but the worktree has no .pre-commit-config.yaml. That config
	// is a gitignored dev-shell symlink present only in the canonical checkout, so a
	// fresh `git worktree add` lacks it and prek aborts every commit with
	// "config file not found" (tc-1zbpk). The env var no-ops prek only in the
	// no-config case — hooks still run normally when a config IS present, so this
	// does not disable enforcement (unlike --no-verify). Stdout/Stderr are wired so
	// a real commit failure surfaces in the run log instead of being swallowed.
	if _, err := ws.runner.Run(ctx, "git", []string{"-C", dir, "commit", "-m", msg}, exec.RunOptions{
		Env:    map[string]string{"PREK_ALLOW_NO_CONFIG": "1"},
		Stdout: out,
		Stderr: out,
	}); err != nil {
		return false, fmt.Errorf("git commit: %w", err)
	}
	fmt.Fprintf(out, "  → %s: bumped workspace input(s): %v\n", name, changed)
	return true, nil
}

// pathClean reports whether path has no unstaged changes in repoDir (git diff
// --quiet exits 0 == clean, 1 == differs). Propagation only ever produces
// working-tree edits to flake.lock (nix writes the file, never the index), so an
// unstaged-only check is sufficient; the tree is clean before propagation runs.
func (ws *Workspace) pathClean(ctx context.Context, repoDir, path string) bool {
	_, err := ws.runner.Run(ctx, "git", []string{"-C", repoDir, "diff", "--quiet", "--", path}, exec.RunOptions{})
	return err == nil
}

// bumpCommitMessage builds the commit message for one or more relocked workspace
// inputs. A single alias gets the canonical one-line "chore(deps): bump <alias>
// <old> -> <new>" form; multiple aliases get a summary subject plus one body
// line per alias.
func bumpCommitMessage(changed []string, before, after map[string]string) string {
	if len(changed) == 1 {
		a := changed[0]
		return fmt.Sprintf("chore(deps): bump %s %s -> %s", a, shortRev(before[a]), shortRev(after[a]))
	}
	msg := "chore(deps): bump workspace inputs\n"
	for _, a := range changed {
		msg += fmt.Sprintf("\n%s %s -> %s", a, shortRev(before[a]), shortRev(after[a]))
	}
	return msg
}
