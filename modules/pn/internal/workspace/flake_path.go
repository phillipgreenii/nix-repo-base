package workspace

import (
	"path/filepath"
)

// defaultFlakeSearchPaths is the ordered list of paths (relative to repo root)
// to search for flake.nix when no explicit flake_path is configured.
// First hit wins. Paths not in this list require explicit flake_path in config.
var defaultFlakeSearchPaths = []string{"flake.nix", "nix/flake.nix"}

// resolveFlakePath returns the relative path to a repo's flake.nix, following
// this priority order:
//
//  1. lock.Repos[repoKey].FlakePath — if set, use it (authoritative runtime value).
//  2. config.Repos[repoKey].FlakePath — explicit config override.
//  3. Search defaultFlakeSearchPaths in order; first hit that exists on disk wins.
//  4. Return "" — no flake.nix found or resolvable for this repo.
//
// The returned path is relative to the repo root (e.g. "flake.nix" or
// "nix/flake.nix" or "custom/dir/flake.nix").
func (ws *Workspace) resolveFlakePath(repoKey string) string {
	// 1. Lock value (authoritative at runtime).
	if ws.lock != nil {
		if entry, ok := ws.lock.Repos[repoKey]; ok && entry.FlakePath != "" {
			return entry.FlakePath
		}
	}

	// 2. Explicit config override.
	if r, ok := ws.config.Repos[repoKey]; ok && r.FlakePath != "" {
		return r.FlakePath
	}

	// 3. Search default paths on disk.
	repoDir := filepath.Join(ws.root, repoKey)
	for _, rel := range defaultFlakeSearchPaths {
		candidate := filepath.Join(repoDir, rel)
		if fileExists(candidate) {
			return rel
		}
	}

	// 4. Not found.
	return ""
}

// isDefaultFlakePath reports whether relPath is one of the default flake
// search paths (and therefore should NOT be written to config).
func isDefaultFlakePath(relPath string) bool {
	for _, d := range defaultFlakeSearchPaths {
		if relPath == d {
			return true
		}
	}
	return false
}
