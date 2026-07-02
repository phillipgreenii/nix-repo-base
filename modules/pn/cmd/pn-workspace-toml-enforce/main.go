// Command pn-workspace-toml-enforce reconciles the two nix-owned keys in a
// pn-workspace.toml — [workspace].id and [hooks.apply].post — against committed
// source values supplied on the command line. It is a small, separate entrypoint
// (NOT a user-facing `pn workspace` verb): pn deliberately stays parse-and-
// surface-only for these keys, while a nix home.activation script invokes THIS
// binary to enforce them. See phillipg-nix-repo-base docs/adr/0017 and
// phillipg-nix-ziprecruiter docs/adr/0049.
//
// It reuses pn's internal/workspace serialization (ParseConfig + the orderedConfig
// writer) so its output is byte-identical to `pn workspace init` / `doctor --fix`;
// it touches ONLY the two keys and preserves [repos.*] and everything else.
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

// run parses flags, enforces the two keys, and returns a process exit code.
// It prints a single line to out ONLY when it rewrote the file (so a no-op is
// silent — the caller's activation wrapper prints nothing on an unchanged run).
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("pn-workspace-toml-enforce", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "workspace root directory containing pn-workspace.toml (required)")
	id := fs.String("id", "", "committed value for [workspace].id (required)")
	applyPost := fs.String("apply-post", "", "committed value for the single [hooks.apply].post entry (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *id == "" || *applyPost == "" {
		fmt.Fprintln(os.Stderr, "error: --root, --id, and --apply-post are all required")
		return 2
	}

	path := filepath.Join(*root, workspace.ConfigFileName)
	changed, err := workspace.EnforceKeys(path, *id, *applyPost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(out, "enforced pn-workspace.toml keys (workspace.id + hooks.apply.post) in %s\n", path)
	}
	return 0
}
