// Command pn-workspace-toml-enforce reconciles the nix-owned keys in a
// pn-workspace.toml — [workspace].id, [hooks.apply].post, and (added by bead
// pg2-k43p.8) the static command templates [workspace].build_command and
// [workspace].apply_command — against committed source values supplied on the
// command line. It is a small, separate entrypoint (NOT a user-facing
// `pn workspace` verb): pn deliberately stays parse-and-surface-only for these
// keys, while a nix home.activation script invokes THIS binary to enforce them.
// See phillipg-nix-repo-base docs/adr/0017 and phillipg-nix-ziprecruiter
// docs/adr/0049.
//
// workspace.terminal is deliberately NOT enforced: pn validates it against
// [repos.*], so it stays pn-owned (repo-topology-coupled). --build-command and
// --apply-command are key-scoped — an empty/omitted value leaves that key
// untouched, so terminal and any future unmanaged key are never disturbed.
//
// It reuses pn's internal/workspace serialization (ParseConfig + the orderedConfig
// writer) so its output is byte-identical to `pn workspace init` / `doctor --fix`;
// it touches ONLY the enforced keys and preserves [repos.*], terminal, and
// everything else.
//
// Behavior: create-if-missing / enforce-when-present against the live file;
// no-op (and silent) when values already match; atomic write preserving the
// file's mode; absent file → no-op (pn workspace init owns creation).
//
// Version is set at build time via -X main.Version=<...> by mkGoBuilders.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run parses flags, enforces the nix-owned keys, and returns a process exit code.
// It prints a single line to out ONLY when it rewrote the file (so a no-op is
// silent — the caller's activation wrapper prints nothing on an unchanged run).
//
// --root, --id, and --apply-post are required. --build-command and
// --apply-command are OPTIONAL and key-scoped: an omitted (empty) value leaves
// that key untouched (see workspace.EnforceKeys).
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("pn-workspace-toml-enforce", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "workspace root directory containing pn-workspace.toml (required)")
	id := fs.String("id", "", "committed value for [workspace].id (required)")
	applyPost := fs.String("apply-post", "", "committed value for the single [hooks.apply].post entry (required)")
	buildCommand := fs.String("build-command", "", "committed value for [workspace].build_command (optional; empty leaves it untouched)")
	applyCommand := fs.String("apply-command", "", "committed value for [workspace].apply_command (optional; empty leaves it untouched)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *id == "" || *applyPost == "" {
		fmt.Fprintln(os.Stderr, "error: --root, --id, and --apply-post are all required")
		return 2
	}

	path := filepath.Join(*root, workspace.ConfigFileName)
	changed, err := workspace.EnforceKeys(path, *id, *applyPost, *buildCommand, *applyCommand)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(out, "enforced pn-workspace.toml nix-owned keys in %s\n", path)
	}
	return 0
}
