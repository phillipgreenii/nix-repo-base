package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// InitOptions configures workspace init behavior.
type InitOptions struct {
	// (reserved for future use, e.g. --no-reconcile)
}

// Init clones missing repos from the TOML's [repos.*] entries, regenerates
// pn-workspace.lock.json with resolved revs, and reconciles existing on-disk
// repos not yet present in the TOML. Clone progress is streamed to out.
func (w *Workspace) Init(ctx context.Context, out io.Writer, opts InitOptions) error {
	// 1. Reconcile: add on-disk repos missing from TOML.
	if err := w.reconcileFromFilesystem(ctx); err != nil {
		return fmt.Errorf("init: reconcile: %w", err)
	}

	// 2. Clone missing repos from TOML (in deterministic name order).
	names := make([]string, 0, len(w.config.Repos))
	for n := range w.config.Repos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		r := w.config.Repos[name]
		repoDir := filepath.Join(w.root, name)
		if isGitRepo(repoDir) {
			continue
		}
		cloneURL := flakeURLToHTTPS(r.URL)
		fmt.Fprintf(out, "  --== clone %s ==--  \n", name)
		if _, err := w.runner.Run(ctx, "git", []string{"clone", "--branch", r.Branch, cloneURL, repoDir}, exec.RunOptions{Dir: w.root, Stdout: out, Stderr: out}); err != nil {
			return fmt.Errorf("init: clone %s: %w", name, err)
		}
	}

	// 3. Write pn-workspace.lock.json with the derived dependency DAG. This
	// needs a terminal flake to derive from; with none configured, write an
	// empty lock.
	if w.config.Workspace.Terminal == "" {
		if err := WriteLock(filepath.Join(w.root, LockFileName), emptyLock()); err != nil {
			return fmt.Errorf("init: write lock: %w", err)
		}
		w.lock = emptyLock()
		return nil
	}
	if err := w.RefreshLock(ctx); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	return nil
}

// RefreshLock re-derives the workspace dependency DAG from each repo's declared
// flake inputs and writes it to pn-workspace.lock.json. It performs no clone
// or reconcile, so it is safe to run any time to regenerate the lock; it backs
// the `pn workspace lock` command and the lock write at the end of init.
func (w *Workspace) RefreshLock(ctx context.Context) error {
	order, _, err := w.deriveDAG(ctx)
	if err != nil {
		return err
	}
	lock := &Lock{
		Order: order,
		Repos: make(map[string]LockRepoEntry),
		Edges: []LockEdge{},
	}
	if err := WriteLock(filepath.Join(w.root, LockFileName), lock); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	w.lock = lock
	return nil
}

// reconcileFromFilesystem scans w.root for existing repo dirs not yet in
// w.config.Repos and adds them to the config (in-memory + on-disk TOML).
func (w *Workspace) reconcileFromFilesystem(ctx context.Context) error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return err
	}
	// Sort entries for deterministic call ordering.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var added bool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".git" || strings.HasPrefix(name, ".") {
			continue
		}
		if _, exists := w.config.Repos[name]; exists {
			continue
		}
		repoDir := filepath.Join(w.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		// Get the remote URL.
		res, err := w.runner.Run(ctx, "git", []string{"-C", repoDir, "remote", "get-url", "origin"}, exec.RunOptions{})
		if err != nil {
			continue
		}
		url := httpsToFlakeURL(strings.TrimSpace(string(res.Stdout)))
		w.config.Repos[name] = RepoConfig{URL: url, Branch: "main"}
		added = true
	}
	if added {
		return w.writeConfigTOML()
	}
	return nil
}

// writeConfigTOML serializes w.config back to pn-workspace.toml at w.root.
// Used by reconciliation to record discovered repos.
func (w *Workspace) writeConfigTOML() error {
	type orderedConfig struct {
		Workspace WorkspaceSection       `toml:"workspace"`
		Repos     map[string]RepoConfig  `toml:"repos"`
		Hooks     map[string]HookCommand `toml:"hooks,omitempty"`
	}
	out := orderedConfig{
		Workspace: w.config.Workspace,
		Repos:     w.config.Repos,
		Hooks:     w.config.Hooks,
	}
	data, err := toml.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.root, ConfigFileName), data, 0o644)
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular() // submodule has .git as file
}

// flakeURLToHTTPS converts e.g. "github:owner/repo" → "https://github.com/owner/repo.git".
// Returns the input unchanged if it doesn't look like a flake-style URL.
func flakeURLToHTTPS(flake string) string {
	if strings.HasPrefix(flake, "github:") {
		spec := strings.TrimPrefix(flake, "github:")
		return "https://github.com/" + spec + ".git"
	}
	return flake
}

// httpsToFlakeURL converts e.g. "https://github.com/owner/repo.git" → "github:owner/repo".
// Returns the input unchanged if it doesn't match the github HTTPS pattern.
func httpsToFlakeURL(https string) string {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(https, prefix) {
		return https
	}
	spec := strings.TrimPrefix(https, prefix)
	spec = strings.TrimSuffix(spec, ".git")
	return "github:" + spec
}
