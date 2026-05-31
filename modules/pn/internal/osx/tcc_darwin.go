//go:build darwin

// Package osx implements darwin-only pn osx subcommands.
//
// On non-darwin platforms the package compiles to nothing; the CLI
// wrapper (cli/osx_darwin.go, Task 13) registers the subcommand only
// when building for darwin.
package osx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// defaultTCCDBSuffix is the user-relative path to the per-user TCC database.
const defaultTCCDBSuffix = "Library/Application Support/com.apple.TCC/TCC.db"

// TCC handles macOS TCC permission checks.
type TCC struct {
	runner exec.Runner
}

// New returns a TCC handler using the given Runner.
func New(runner exec.Runner) *TCC {
	return &TCC{runner: runner}
}

// CheckOptions configures Check.
type CheckOptions struct {
	// DBPath overrides the TCC database path. Empty falls back to
	// $TCC_DB_PATH or the per-user default. Used for testing.
	DBPath string
}

// Check inspects macOS TCC entries for duplicate Nix-store-path clients and
// reports stale entries. Mirrors pn-osx-tcc-check.sh.
//
// TODO: port the awk-based grouping/output logic (group by
// service+binary basename, mark newest as current, format the section
// headers). The current implementation captures the sqlite3 subprocess
// seam (probe + query). Integration tests (Task 14) drive parity with
// the bash output format.
func (t *TCC) Check(ctx context.Context, opts CheckOptions) error {
	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = os.Getenv("TCC_DB_PATH")
	}
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate home dir: %w", err)
		}
		dbPath = filepath.Join(home, defaultTCCDBSuffix)
	}

	// Probe Full Disk Access — bash uses `sqlite3 $TCC_DB "SELECT 1 FROM access LIMIT 1"`
	// and treats failure as "FDA not granted, skip check (exit 0)".
	if _, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, "SELECT 1 FROM access LIMIT 1"}, exec.RunOptions{}); err != nil {
		// FDA not granted; bash exits 0 with a warning. Mirror that behavior.
		return nil
	}

	// Query duplicates. Bash pipes the result through awk to group/format.
	// The Go port leaves output formatting to a follow-up (see TODO above).
	const query = "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"
	if _, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, query}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("tcc duplicates query: %w", err)
	}
	return nil
}
