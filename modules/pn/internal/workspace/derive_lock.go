package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// deriveLock computes a Lock from scratch by:
//  1. Running gatherInputURLs to eval each repo's flake inputs.
//  2. Building edges via buildEdges.
//  3. Populating per-repo LockRepoEntry (FlakePath, RemoteURL).
//  4. Resolving the terminal via resolveTerminal.
//
// flagTerminal, when non-empty, is used as the terminal override (tier 1 of
// the priority order: flag > config > auto-detect).
//
// Returns:
//   - lock: the most complete Lock derivable (always non-nil, even with errors).
//   - validErrs: non-fatal validation issues (missing_terminal, terminal_not_sink, etc.).
//   - err: non-nil only on fatal I/O errors (config not readable, etc.).
func deriveLock(ctx context.Context, ws *Workspace, flagTerminal string) (*Lock, []ValidationError, error) {
	// Gather input URLs from every repo's flake.
	inputURLs, err := ws.gatherInputURLs(ctx)
	if err != nil {
		return emptyLock(), nil, fmt.Errorf("deriveLock: gather input URLs: %w", err)
	}

	// Build edges from URL matching.
	edges, order, err := buildEdges(ws.config.Repos, inputURLs)
	if err != nil {
		return emptyLock(), nil, fmt.Errorf("deriveLock: build edges: %w", err)
	}

	// Build per-repo lock entries.
	repos := make(map[string]LockRepoEntry, len(ws.config.Repos))
	for key, rc := range ws.config.Repos {
		fp := ws.resolveFlakePath(key)
		repos[key] = LockRepoEntry{
			FlakePath: fp,
			RemoteURL: displayURL(rc),
		}
	}

	// Resolve terminal via 3-tier priority (flag > config > auto-detect).
	terminal, validErrs, err := resolveTerminal(ws.config, flagTerminal, edges, repos)
	if err != nil {
		// Fatal only if flag validation failed (unknown repo).
		return emptyLock(), nil, fmt.Errorf("deriveLock: resolve terminal: %w", err)
	}

	// If no terminal found, emit missing_terminal validation error.
	if terminal == "" {
		validErrs = append(validErrs, ValidationError{
			Code:    "missing_terminal",
			Message: "workspace terminal cannot be determined: set workspace.terminal in pn-workspace.toml or ensure exactly one repo is a unique graph sink",
		})
	}

	lock := &Lock{
		Terminal: terminal,
		Order:    order,
		Repos:    repos,
		Edges:    edges,
	}

	return lock, validErrs, nil
}

// effectiveLock returns the best available lock for the workspace:
//  1. If the disk lock (ws.lock) covers exactly the configured repo set,
//     return it immediately (no nix eval).
//  2. Otherwise, derive a fresh lock via deriveLock and return it along
//     with any validation errors.
//
// Commands that require a terminal (build, apply) must check validErrs and
// surface terminal-related errors to the user. Commands that don't need the
// lock's terminal (rebase, push, status) may ignore validErrs.
func (ws *Workspace) effectiveLock(ctx context.Context) (*Lock, []ValidationError, error) {
	if ws.lock != nil && lockMatchesConfig(ws.lock, ws.config) {
		return ws.lock, nil, nil
	}
	lock, validErrs, err := deriveLock(ctx, ws, "")
	if err != nil {
		return emptyLock(), nil, err
	}
	return lock, validErrs, nil
}

// lockMatchesConfig reports whether the lock's Repos map covers exactly the
// configured repo set (same keys, no more, no fewer).
func lockMatchesConfig(lock *Lock, cfg *WorkspaceConfig) bool {
	if lock == nil || cfg == nil {
		return false
	}
	if len(lock.Repos) != len(cfg.Repos) {
		return false
	}
	for key := range cfg.Repos {
		if _, ok := lock.Repos[key]; !ok {
			return false
		}
	}
	return true
}

// WriteDerivedLock derives the workspace lock, validates it, and writes it
// atomically to <dir>/pn-workspace.lock.json via a tempfile+rename pattern.
// If any ValidationError is present, no file is written and an error is returned
// describing the issues (the previous lock file, if any, is preserved).
//
// After a successful write, if the legacy pn-workspace.lock exists in the same
// directory, it is removed and a notice is written to out (may be nil to silence).
func (ws *Workspace) WriteDerivedLock(ctx context.Context, dir string) error {
	return ws.WriteDerivedLockTo(ctx, dir, nil, "")
}

// WriteDerivedLockTo is like WriteDerivedLock but accepts an io.Writer for notices
// (legacy lock removal) and a flagTerminal override. Pass nil out to suppress
// notices. Pass "" for flagTerminal to use the config or auto-detect.
func (ws *Workspace) WriteDerivedLockTo(ctx context.Context, dir string, out io.Writer, flagTerminal string) error {
	lock, validErrs, err := deriveLock(ctx, ws, flagTerminal)
	if err != nil {
		return err
	}
	if len(validErrs) > 0 {
		// Surface all validation errors but do NOT write.
		var msgs string
		for i, ve := range validErrs {
			if i > 0 {
				msgs += "; "
			}
			msgs += ve.Message
		}
		return fmt.Errorf("lock validation failed: %s", msgs)
	}

	lockPath := filepath.Join(dir, LockFileName)
	if err := writeLockAtomic(lockPath, lock); err != nil {
		return err
	}

	// Remove legacy lock if present.
	legacyPath := filepath.Join(dir, LockFileNameLegacy)
	if _, err := os.Stat(legacyPath); err == nil {
		if rmErr := os.Remove(legacyPath); rmErr == nil {
			if out != nil {
				fmt.Fprintf(out, "removed legacy %s (replaced by %s)\n", LockFileNameLegacy, LockFileName)
			}
		}
	}

	return nil
}

// writeLockAtomic writes lock to destPath atomically:
// 1. Create a tempfile in the same directory as destPath.
// 2. Write lock to tempfile using WriteLock (which writes JSON).
// 3. Rename tempfile to destPath (atomic on POSIX).
// 4. On any error, remove the tempfile.
func writeLockAtomic(destPath string, lock *Lock) error {
	dir := filepath.Dir(destPath)
	// Create an empty tempfile to get a unique name; WriteLock will overwrite it.
	tmp, err := os.CreateTemp(dir, ".pn-lock-*.tmp")
	if err != nil {
		return fmt.Errorf("write lock (tempfile create): %w", err)
	}
	tmpPath := tmp.Name()
	// Close before WriteLock re-opens it.
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write lock (tempfile close): %w", err)
	}

	// Write to tmpPath via WriteLock.
	if err := WriteLock(tmpPath, lock); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Atomic rename: replace destPath with tmpPath.
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write lock (rename %s -> %s): %w", tmpPath, destPath, err)
	}
	return nil
}
