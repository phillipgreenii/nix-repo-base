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
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// defaultTCCDBSuffix is the user-relative path to the per-user TCC database.
const defaultTCCDBSuffix = "Library/Application Support/com.apple.TCC/TCC.db"

// tccFDAProbeQuery checks Full Disk Access: a trivial SELECT against the TCC
// database. A non-zero exit means the terminal lacks FDA.
const tccFDAProbeQuery = "SELECT 1 FROM access LIMIT 1"

// tccDuplicatesQuery selects enabled (auth_value = 2) Nix-store-path TCC entries,
// ordered for deterministic grouping. Defined as a constant so the production
// code and tests reference the exact same string.
const tccDuplicatesQuery = "SELECT service, client, last_modified FROM access WHERE client LIKE '/nix/store/%' AND auth_value = 2 ORDER BY service, client;"

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
// The duplicate report (or the "no duplicates" message) is written to out; the
// FDA-not-granted warning is written to errOut. Both are injected by the CLI
// from cmd.OutOrStdout() / cmd.ErrOrStderr().
func (t *TCC) Check(ctx context.Context, out, errOut io.Writer, opts CheckOptions) error {
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
	if _, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, tccFDAProbeQuery}, exec.RunOptions{}); err != nil {
		// FDA not granted; warn on errOut and exit 0, mirroring the bash skip.
		fmt.Fprint(errOut, "⚠️  TCC check skipped — terminal lacks Full Disk Access\n"+
			"   Grant FDA: System Preferences > Privacy & Security > Full Disk Access > [your terminal]\n")
		return nil
	}

	// Query enabled Nix-store duplicates, then group/format in Go (the awk port).
	res, err := t.runner.Run(ctx, "sqlite3", []string{dbPath, tccDuplicatesQuery}, exec.RunOptions{})
	if err != nil {
		return fmt.Errorf("tcc duplicates query: %w", err)
	}

	groups := groupTCCEntries(parseTCCRows(res.Stdout))
	if len(groups) == 0 {
		fmt.Fprintln(out, "✅ No TCC duplicates found")
		return nil
	}
	fmt.Fprint(out, formatTCCReport(groups))
	return nil
}
