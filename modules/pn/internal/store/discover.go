package store

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ─── Fixed profile paths ──────────────────────────────────────────────────────

func (s *Store) systemProfile() string { return "/nix/var/nix/profiles/system" }

func (s *Store) homeManagerProfile() string {
	return filepath.Join(s.env.Home, ".local/state/nix/profiles/home-manager")
}

// ─── userProfiles ─────────────────────────────────────────────────────────────

// genLinkRE matches generation-link suffixes like -195-link.
var genLinkRE = regexp.MustCompile(`-[0-9]+-link$`)

// userProfiles returns entries directly under ~/.local/state/nix/profiles,
// excluding "home-manager" and names matching -[0-9]+-link$. Mirrors bash
// discover_user_profiles.
func (s *Store) userProfiles() []string {
	dir := filepath.Join(s.env.Home, ".local/state/nix/profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if name == "home-manager" || genLinkRE.MatchString(name) {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out
}

// ─── devbox fixed profiles ────────────────────────────────────────────────────

// devboxGlobalProfile returns the devbox global profile path if it exists.
// Mirrors bash discover_devbox_global_profile.
func (s *Store) devboxGlobalProfile() string {
	path := filepath.Join(s.env.Home, ".local/share/devbox/global/default/.devbox/nix/profile/default")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// devboxUtilProfile returns the devbox util profile path if it exists.
// Mirrors bash discover_devbox_util_profile.
func (s *Store) devboxUtilProfile() string {
	path := filepath.Join(s.env.Home, ".local/share/devbox/util/.devbox/nix/profile/default")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// ─── devboxProjects ───────────────────────────────────────────────────────────

// devboxProjects discovers .devbox/nix/profile/default paths under the given
// search dirs, following git worktrees. Missing dirs emit a WARNING to errOut.
// Depth limits: .git dirs at maxDepth 4, devbox profiles at maxDepth 5.
// Mirrors bash discover_devbox_projects.
func (s *Store) devboxProjects(ctx context.Context, errOut io.Writer, searchDirs []string) []string {
	seen := make(map[string]bool)
	var allDirs []string

	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); err != nil {
			fmt.Fprintf(errOut, "WARNING: search dir does not exist: %s\n", dir)
			continue
		}
		allDirs = append(allDirs, dir)

		// Walk depth≤4 for .git directories, then run git worktree list --porcelain.
		gitRepos := walkForGitRepos(dir, 4)
		for _, repo := range gitRepos {
			worktrees := s.gitWorktrees(ctx, repo)
			for _, wt := range worktrees {
				// Skip the repo dir itself (already in allDirs).
				if wt == repo {
					continue
				}
				if _, err := os.Stat(wt); err == nil {
					allDirs = append(allDirs, wt)
				}
			}
		}
	}

	var out []string
	for _, searchDir := range allDirs {
		if _, err := os.Stat(searchDir); err != nil {
			continue
		}
		profiles := walkForDevboxProfiles(searchDir, 5)
		for _, p := range profiles {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// walkForGitRepos walks root at maxDepth, returning parent dirs of .git entries.
func walkForGitRepos(root string, maxDepth int) []string {
	var repos []string
	rootDepth := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		depth := strings.Count(path, string(filepath.Separator)) - rootDepth
		if d.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if d.IsDir() && d.Name() == ".git" {
			repos = append(repos, filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
	return repos
}

// walkForDevboxProfiles walks root at maxDepth for */.devbox/nix/profile/default.
func walkForDevboxProfiles(root string, maxDepth int) []string {
	var profiles []string
	rootDepth := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		depth := strings.Count(path, string(filepath.Separator)) - rootDepth
		if d.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, "/.devbox/nix/profile/default") {
			profiles = append(profiles, path)
		}
		return nil
	})
	return profiles
}

// gitWorktrees runs `git -C repo worktree list --porcelain` via the runner
// and returns all worktree paths (including the main repo).
func (s *Store) gitWorktrees(ctx context.Context, repo string) []string {
	result, err := s.runner.Run(ctx, "git",
		[]string{"-C", repo, "worktree", "list", "--porcelain"},
		exec.RunOptions{})
	if err != nil {
		return nil
	}
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(result.Stdout))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths
}

// ─── discoverResultSymlinks ───────────────────────────────────────────────────

// discoverResultSymlinks finds symlinks named "result" or "result-*" whose
// target starts with "/nix/store/" under the given search dirs (depth≤3).
// Uses os.Readlink (single-hop), NOT filepath.EvalSymlinks — targets are real
// /nix/store/... paths that do not exist in test environments; EvalSymlinks
// would error and drop them.
func discoverResultSymlinks(searchDirs []string) []string {
	var out []string
	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		symlinks := walkSymlinks(dir, 3, func(rel string) bool {
			base := filepath.Base(rel)
			return base == "result" || strings.HasPrefix(base, "result-")
		}, "/nix/store/")
		out = append(out, symlinks...)
	}
	return out
}

// ─── nhTempRoots ──────────────────────────────────────────────────────────────

// nhTempRoots finds symlinks at depth≤2 under tmpDir() whose path matches
// */nh-darwin*/result (parent dir name starts with "nh-darwin", basename is
// "result"). Mirrors bash discover_nh_temp_roots.
func (s *Store) nhTempRoots() []string {
	tmp := s.env.tmpDir()
	return walkSymlinks(tmp, 2, func(rel string) bool {
		// rel is e.g. "nh-darwinABCDEF/result"
		dir := filepath.Dir(rel)
		base := filepath.Base(rel)
		if base != "result" {
			return false
		}
		// dir must be a single path component starting with "nh-darwin"
		// (i.e., the parent is directly under tmp, not nested deeper)
		if strings.Contains(dir, string(filepath.Separator)) {
			return false
		}
		return strings.HasPrefix(dir, "nh-darwin")
	}, "")
}

// ─── walkSymlinks ─────────────────────────────────────────────────────────────

// walkSymlinks returns symlinks under root up to maxDepth whose RELATIVE PATH
// (from root) satisfies pathMatch, and (when targetPrefix != "") whose
// os.Readlink target has that prefix. pathMatch receives the path relative to
// root so callers can express both basename rules (result / result-*) AND
// path-component rules (*/nh-darwin*/result). Targets are read with os.Readlink
// (single-hop), never EvalSymlinks, so dangling /nix/store targets still match.
func walkSymlinks(root string, maxDepth int, pathMatch func(rel string) bool, targetPrefix string) []string {
	var out []string
	rootDepth := strings.Count(root, string(filepath.Separator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		depth := strings.Count(path, string(filepath.Separator)) - rootDepth
		if d.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		// Check if this is a symlink using Lstat (WalkDir uses lstat for the entry type).
		fi, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return nil
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if !pathMatch(rel) {
			return nil
		}
		if targetPrefix != "" {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return nil
			}
			if !strings.HasPrefix(target, targetPrefix) {
				return nil
			}
		}
		out = append(out, path)
		return nil
	})
	return out
}

// ─── staleNixProfiles ─────────────────────────────────────────────────────────

// lstatModTime is the seam for symlink mtime (overridden in tests to inject
// controlled timestamps without touching the filesystem clock).
// Production code uses os.Lstat.ModTime().
var lstatModTime = func(path string) (time.Time, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// staleNixProfiles returns symlinks directly under ~/.nix-profiles whose mtime
// is older than now-keepDays*24h. keepDays==0 returns all symlinks.
//
// Note: bash `find -mtime +N` truncates partial days (selects age ≥ (N+1)×24h);
// we use strict now-keepDays*24h. This ≤24h boundary difference is intentional
// — it does not affect real-world use where links are days/weeks past threshold.
func (s *Store) staleNixProfiles(keepDays int, now time.Time) []string {
	dir := filepath.Join(s.env.Home, ".nix-profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, lstatErr := os.Lstat(p)
		if lstatErr != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if keepDays == 0 {
			out = append(out, p)
			continue
		}
		mt, mtErr := lstatModTime(p)
		if mtErr != nil {
			continue
		}
		if mt.Before(cutoff) {
			out = append(out, p)
		}
	}
	return out
}

// ─── homeManagerGenLinks ──────────────────────────────────────────────────────

// homeManagerGenLinks returns symlinks matching home-manager-*-link at depth 1
// under ~/.local/state/nix/profiles. Mirrors the bash:
//
//	find "$hm_dir" -maxdepth 1 -name "${hm_name}-*-link" -type l
func (s *Store) homeManagerGenLinks() []string {
	dir := filepath.Join(s.env.Home, ".local/state/nix/profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "home-manager-") || !strings.HasSuffix(name, "-link") {
			continue
		}
		p := filepath.Join(dir, name)
		fi, lstatErr := os.Lstat(p)
		if lstatErr != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ─── formatProfileLabel ───────────────────────────────────────────────────────

// formatProfileLabel returns a short user-friendly label for a profile in a
// given category. Mirrors bash format_profile_label.
//
// devbox-projects: profile path is <project>/.devbox/nix/profile/default —
// climb 4 dirnames to reach the project dir, then apply ~ substitution.
func (s *Store) formatProfileLabel(profile, category string) string {
	switch category {
	case "system", "home-manager", "devbox-global", "devbox-util":
		return category
	case "user-profiles":
		return filepath.Base(profile)
	case "devbox-projects":
		// Climb 4 dirs: default → profile → nix → .devbox → project
		projDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(profile))))
		if s.env.Home != "" && strings.HasPrefix(projDir, s.env.Home+string(filepath.Separator)) {
			return "~" + strings.TrimPrefix(projDir, s.env.Home)
		}
		if s.env.Home != "" && projDir == s.env.Home {
			return "~"
		}
		return projDir
	default:
		return profile
	}
}

// ─── isOrphanedStandaloneHMProfile ────────────────────────────────────────────

// isOrphanedStandaloneHMProfile returns true when hmProfile is a symlink AND
// ~/.local/state/home-manager/gcroots/current-home is also a symlink.
// Mirrors bash is_orphaned_standalone_hm_profile.
func (s *Store) isOrphanedStandaloneHMProfile(hmProfile string) bool {
	fi, err := os.Lstat(hmProfile)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	current := filepath.Join(s.env.Home, ".local/state/home-manager/gcroots/current-home")
	fi, err = os.Lstat(current)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}
