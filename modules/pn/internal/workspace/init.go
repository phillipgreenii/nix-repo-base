package workspace

import (
	"context"
	"fmt"
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
// pn-workspace.lock with resolved revs, and reconciles existing on-disk repos
// not yet present in the TOML.
func (w *Workspace) Init(ctx context.Context, opts InitOptions) error {
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
		if _, err := w.runner.Run(ctx, "git", []string{"clone", "--branch", r.Branch, cloneURL, repoDir}, exec.RunOptions{Dir: w.root}); err != nil {
			return fmt.Errorf("init: clone %s: %w", name, err)
		}
	}

	// 3. Write pn-workspace.lock with resolved revs (alphabetical order).
	lock := &Lock{Repos: make(map[string]LockedRepo, len(w.config.Repos))}
	for _, name := range names {
		r := w.config.Repos[name]
		repoDir := filepath.Join(w.root, name)
		res, err := w.runner.Run(ctx, "git", []string{"-C", repoDir, "rev-parse", "HEAD"}, exec.RunOptions{})
		if err != nil {
			return fmt.Errorf("init: rev-parse %s: %w", name, err)
		}
		rev := strings.TrimSpace(string(res.Stdout))
		lock.Repos[name] = LockedRepo{URL: r.URL, Rev: rev}
	}
	if err := WriteLock(filepath.Join(w.root, LockFileName), lock); err != nil {
		return fmt.Errorf("init: write lock: %w", err)
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
