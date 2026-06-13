package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// InitOptions configures workspace init behavior.
type InitOptions struct {
	// Terminal is accepted for uniformity with other commands but is currently
	// a no-op for Init (Init is config-only and does not need a terminal).
	Terminal string
}

// Init reconciles on-disk repos not yet in the TOML config, resolves flake
// paths for all repos, and writes pn-workspace.toml atomically. It does NOT
// clone repos and does NOT write a workspace lock.
//
// Init is idempotent: running twice in succession produces "no changes" on the
// second run. It never errors on indeterminacy (no terminal, missing repos) —
// those are the lock command's concern.
//
// A per-change summary is written to out.
func (w *Workspace) Init(ctx context.Context, out io.Writer, opts InitOptions) error {
	var changes int32 // number of config changes made

	// 1. Reconcile: scan workspace root for git repos not yet in config.
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return fmt.Errorf("init: read workspace dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".git" || strings.HasPrefix(name, ".") {
			continue
		}
		if _, exists := w.config.Repos[name]; exists {
			// Already in config; skip remote discovery (do not overwrite URL).
			continue
		}
		repoDir := filepath.Join(w.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		// Discover URL from git remote origin.
		res, err := w.runner.Run(ctx, "git",
			[]string{"-C", repoDir, "remote", "get-url", "origin"},
			exec.RunOptions{})
		if err != nil {
			// Remote not configured; add entry with empty URL to be filled later.
			w.config.Repos[name] = RepoConfig{Branch: "main"}
			atomic.AddInt32(&changes, 1)
			fmt.Fprintf(out, "added repo %s (no origin remote; set url manually)\n", name)
			continue
		}
		url := httpsToFlakeURL(strings.TrimSpace(string(res.Stdout)))
		w.config.Repos[name] = RepoConfig{URL: url, Branch: "main"}
		atomic.AddInt32(&changes, 1)
		fmt.Fprintf(out, "added repo %s (url: %s)\n", name, url)
	}

	// 2. Resolve flake_path for every repo; persist non-defaults to config.
	for _, name := range orderedRepoNames(w.config.Repos) {
		r := w.config.Repos[name]
		if r.FlakePath != "" {
			// Config already has an explicit flake_path; preserve it (never overwrite).
			continue
		}
		resolved := w.resolveFlakePath(name)
		if resolved == "" {
			// Not found among defaults; skip (user must configure manually).
			continue
		}
		if isDefaultFlakePath(resolved) {
			// Default location; no need to persist to config.
			continue
		}
		// Non-default path found; write to config.
		r.FlakePath = resolved
		w.config.Repos[name] = r
		atomic.AddInt32(&changes, 1)
		fmt.Fprintf(out, "set flake_path for %s: %s\n", name, resolved)
	}

	// 3. Write pn-workspace.toml atomically if anything changed.
	if changes == 0 {
		fmt.Fprintln(out, "no changes")
		return nil
	}
	if err := w.writeConfigTOMLAtomic(); err != nil {
		return fmt.Errorf("init: write config: %w", err)
	}
	return nil
}

// writeConfigTOMLAtomic serializes w.config to pn-workspace.toml at w.root
// using a tempfile+rename pattern (atomic on POSIX). Key order: workspace
// section first, repos sorted by name, hooks last.
func (w *Workspace) writeConfigTOMLAtomic() error {
	// Build ordered output struct. toml.Marshal preserves the struct field
	// order; repos come out sorted because we collect them that way.
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
	dest := filepath.Join(w.root, ConfigFileName)
	tmp, err := os.CreateTemp(w.root, ".pn-config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config (tempfile): %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write config (write): %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (close): %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (rename): %w", err)
	}
	return nil
}

// writeConfigTOML serializes w.config back to pn-workspace.toml at w.root.
// Used by reconciliation to record discovered repos. Kept for backward
// compatibility; new code should prefer writeConfigTOMLAtomic.
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
