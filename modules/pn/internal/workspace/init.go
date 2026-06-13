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

// Init reconciles on-disk repos not yet in TOML, clones any missing repos,
// then writes pn-workspace.lock.json with the derived dependency DAG.
// Clone progress is streamed to out.
//
// After cloning, Init also resolves each repo's flake path and writes
// flake_path to config for any non-default locations discovered.
//
// NOTE: the clone step is performed via Clone() to avoid duplication; this
// means Init remains idempotent. The tc-perh.9.11 slice will make Init
// config-only (removing the clone and lock steps from here).
func (w *Workspace) Init(ctx context.Context, out io.Writer, opts InitOptions) error {
	// 1. Reconcile: add on-disk repos missing from TOML.
	if err := w.reconcileFromFilesystem(ctx); err != nil {
		return fmt.Errorf("init: reconcile: %w", err)
	}

	// 2. Clone missing repos (delegates to Clone so the logic lives in one place).
	if err := w.Clone(ctx, out, CloneOptions{}); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// 3. Resolve flake paths for all repos and persist non-defaults to config.
	if err := w.persistNonDefaultFlakePaths(); err != nil {
		return fmt.Errorf("init: persist flake paths: %w", err)
	}

	// 4. Write pn-workspace.lock.json with the derived dependency DAG. This
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

// persistNonDefaultFlakePaths resolves each repo's flake path and writes it
// to pn-workspace.toml if (and only if) it is non-default.
// Default paths (flake.nix, nix/flake.nix) are NOT written — they remain implicit.
func (w *Workspace) persistNonDefaultFlakePaths() error {
	var changed bool
	names := make([]string, 0, len(w.config.Repos))
	for n := range w.config.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		r := w.config.Repos[name]
		if r.FlakePath != "" {
			// Already explicitly set; don't override.
			continue
		}
		resolved := w.resolveFlakePath(name)
		if resolved == "" || isDefaultFlakePath(resolved) {
			// No flake found, or it's a default path — don't write to config.
			continue
		}
		// Non-default path: persist to config.
		r.FlakePath = resolved
		w.config.Repos[name] = r
		changed = true
	}
	if changed {
		return w.writeConfigTOML()
	}
	return nil
}

// RefreshLock re-derives the workspace lock from each repo's declared flake
// inputs and writes it to pn-workspace.lock.json. Uses the new gatherInputURLs
// + buildEdges approach (replacing the old gatherDeclaredInputs + buildDAG).
func (w *Workspace) RefreshLock(ctx context.Context) error {
	inputURLs, err := w.gatherInputURLs(ctx)
	if err != nil {
		return fmt.Errorf("gather input URLs: %w", err)
	}

	edges, order, err := buildEdges(w.config.Repos, inputURLs)
	if err != nil {
		return err
	}

	lock := &Lock{
		Order: order,
		Repos: make(map[string]LockRepoEntry),
		Edges: edges,
	}
	if err := WriteLock(filepath.Join(w.root, LockFileName), lock); err != nil {
		return fmt.Errorf("write lock: %w", err)
	}
	w.lock = lock
	return nil
}

// reconcileFromFilesystem scans w.root for existing repo dirs not yet in
// w.config.Repos and adds them to the config (in-memory + on-disk TOML).
// For each newly-added repo, it also resolves the flake_path and records
// non-default paths in the config.
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
		newEntry := RepoConfig{URL: url, Branch: "main"}
		w.config.Repos[name] = newEntry
		added = true
	}

	if !added {
		return nil
	}

	// Resolve flake paths for newly-added repos and persist non-defaults.
	for name, r := range w.config.Repos {
		if r.FlakePath != "" {
			continue // already set
		}
		resolved := w.resolveFlakePath(name)
		if resolved != "" && !isDefaultFlakePath(resolved) {
			r.FlakePath = resolved
			w.config.Repos[name] = r
		}
	}

	return w.writeConfigTOML()
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
