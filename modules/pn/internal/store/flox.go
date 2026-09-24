package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ─── Flox ───────────────────────────────────────────────────────────────────
//
// Flox has no local equivalent of an old nix-env generation to prune.
// `flox generations` is exclusively a FloxHub (cloud) feature: running it
// against a purely local (never-pushed) environment fails outright --
// verified live, flox 1.17.0-gd37edfd (bead pg2-8k05k):
//
//	✘ ERROR: Generations are only available for environments pushed to floxhub.
//	The environment <name> is a local only environment.
//
// Locally, each Flox-managed project keeps exactly one live build per
// environment name, already correctly GC-rooted via a
// .flox/run/<system>.<name> indirect root -- confirmed present and current
// for this workspace's own projects during the same triage, and
// /nix/var/nix/gcroots/auto/ already points back at those symlinks. So
// there is nothing analogous to devbox's old generations to prune here (the
// earlier idea of symlinking ~/.local/share/flox/environments/ into
// /nix/var/nix/gcroots/flox-environments was independently verified to be
// unnecessary AND to target a path that doesn't exist on this machine --
// deliberately NOT implemented).
//
// This file only DISCOVERS Flox's on-disk footprint so deepclean's summary
// can surface it (reportFlox in deepclean.go). Nothing it finds is ever
// deleted.

// floxGlobalPaths returns Flox's two fixed global data directories:
// ~/.local/share/flox (env registry / metadata, XDG_DATA_HOME) and
// ~/.cache/flox (auth token cache, metrics, per-activation process cache,
// XDG_CACHE_HOME).
func (s *Store) floxGlobalPaths() []string {
	return []string{
		filepath.Join(s.env.Home, ".local/share/flox"),
		filepath.Join(s.env.Home, ".cache/flox"),
	}
}

// floxEnvironments discovers .flox environment directories directly under
// the given search dirs (maxDepth 4, matching the .git-detection depth used
// by devboxProjects -- a project's .flox sits at the same depth as its
// .git). Unlike devboxProjects, this does NOT also follow git worktrees:
// this section is report-only (nothing it finds is ever pruned), so a
// second `git worktree list` invocation per repo on every deepclean run
// isn't warranted for it. Missing search dirs are skipped silently -- the
// Devbox Projects section (which runs earlier over the same
// cfg.SearchDirs) already warns about those to errOut, and duplicating
// that warning here would just be confusing.
func (s *Store) floxEnvironments(searchDirs []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		for _, p := range walkForFloxDirs(dir, 4) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// walkForFloxDirs walks root at maxDepth, returning directories named
// ".flox". Mirrors walkForGitRepos's shape (discover.go) but matches
// ".flox" and returns the matched directory itself (not its parent), since
// callers size the .flox directory directly.
func walkForFloxDirs(root string, maxDepth int) []string {
	var dirs []string
	rootDepth := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		depth := strings.Count(path, string(filepath.Separator)) - rootDepth
		if d.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if d.IsDir() && d.Name() == ".flox" {
			dirs = append(dirs, path)
			return filepath.SkipDir
		}
		return nil
	})
	return dirs
}
