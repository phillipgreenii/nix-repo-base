package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// ─── Flox process-cache temp-dir litter (bead pg2-td8ut) ───────────────────
//
// Separately from the generation-history question above, ~/.cache/flox/process/
// accumulates orphaned `.tmpXXXXXX` staging directories over time. Root cause
// (observable evidence only -- Flox is closed-source; nothing here reads its
// source, only its installed binary's own embedded strings and this machine's
// live state):
//
//   - flox-activations' embedded strings (cli/flox-core/src/activations.rs)
//     include "too many temporary files exist" -- the Rust `tempfile` crate's
//     own retry-exhaustion message -- alongside "state_dir", "no existing
//     activation state, creating new one", and "failed to remove start state
//     dir after detach". This is consistent with activation bookkeeping
//     staging a new activation's state as a `tempfile`-created dir (default
//     naming: ".tmp" + 6 random alphanumeric chars, exactly what is observed
//     on disk) intended to be populated then atomically moved into place
//     under ~/.cache/flox/run/activations/<id>/ (where this machine's one
//     currently-active activation lock actually lives: state.json +
//     state.lock, confirmed via `ps`+`lsof` -- a different path entirely).
//   - If the owning process exits (crash, signal, interrupted shell) before
//     that final move, the staging dir is orphaned in ~/.cache/flox/process/
//     forever -- nothing ever revisits that directory to clean it up.
//
// Confirmed live on this machine (2026-09-24, flox 1.17.0-gd37edfd): 1359 such
// directories dating back to May, still growing (~30/day recently, 2 in the
// preceding hour alone) -- i.e. reproducible right now, not a one-time
// historical artifact. Every single one is recursively empty: 0 regular
// files, 0 symlinks, at most one nested empty `.tmpXXXXXX` subdirectory (3 of
// the 1359 had exactly that). `lsof` found zero open file descriptors into
// ~/.cache/flox/process/ at research time. `flox gc` ("Garbage collects any
// data for deleted environments") does not cover this -- it targets
// per-environment registry data, not this staging path -- and no other flox
// subcommand does either.
//
// Safety: a directory this young could plausibly be mid-creation by an
// in-flight `flox activate`, so removal requires BOTH conditions, never age
// alone: recursively empty (dirEmptyRecursive; a genuinely in-flight
// activation still populating its staging dir is never touched, regardless
// of age) AND older than the keepDays cutoff -- reusing the same `keep_days`
// / `--keep-since` knob staleNixProfiles already uses for an analogous
// mtime-staleness judgment (keepDays==0 disables the age check, matching
// staleNixProfiles' own keepDays==0 semantics).

// floxProcessDir returns ~/.cache/flox/process, the staging directory for
// orphaned Flox activation temp dirs.
func (s *Store) floxProcessDir() string {
	return filepath.Join(s.env.Home, ".cache/flox/process")
}

// floxTempDirRE matches the Rust `tempfile` crate's default naming for a
// directory created with prefix ".tmp": ".tmp" followed by 6+ alphanumeric
// characters.
var floxTempDirRE = regexp.MustCompile(`^\.tmp[A-Za-z0-9]+$`)

// floxProcessTempDirs discovers orphaned `.tmpXXXXXX` staging directories
// directly under ~/.cache/flox/process/ that are safe to remove: recursively
// empty AND (when keepDays != 0) older than now-keepDays days. A missing
// ~/.cache/flox/process/ (Flox never used, or already clean) returns nil.
func (s *Store) floxProcessTempDirs(keepDays int, now time.Time) []string {
	dir := s.floxProcessDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !floxTempDirRE.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, statErr := os.Lstat(p)
		if statErr != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue // never follow/treat a symlink here
		}
		if keepDays != 0 && fi.ModTime().After(cutoff) {
			continue // too young to be confident it's abandoned, not in-flight
		}
		if !dirEmptyRecursive(p) {
			continue // has real content; never touch
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// dirEmptyRecursive reports whether dir contains nothing but (optionally
// nested) empty directories -- no regular files, symlinks, or any other
// non-directory entry anywhere in its tree.
func dirEmptyRecursive(dir string) bool {
	empty := true
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			empty = false
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if !d.IsDir() {
			empty = false
			return filepath.SkipAll
		}
		return nil
	})
	return empty
}
