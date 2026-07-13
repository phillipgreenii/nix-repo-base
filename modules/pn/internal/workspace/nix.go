package workspace

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// denyListedNixSubcommands is the set of `nix <subcommand>` invocations that
// cannot meaningfully coexist with --override-input. Lock-mutating commands
// (flake update, flake lock) are incoherent under workspace overrides —
// they would alter the lock based on overridden, in-flux input paths rather
// than the declared upstream URLs. We refuse to run them via `pn workspace
// nix`; users who genuinely want to update locks should run `nix` directly.
//
// Matched against the de-flagged command tokens (option-like tokens AND their
// consumed values removed) as a contiguous run anywhere: e.g. {"flake",
// "update"} matches `flake update`, `--verbose flake update`,
// `--option build-cores 4 flake update`, `flake --verbose update`, and
// `flake --option build-cores 4 update` alike (beads pg2-odu4p, pg2-9p527).
var denyListedNixSubcommands = [][]string{
	{"flake", "update"},
	{"flake", "lock"},
}

// NixCommand runs `nix <args>` from the workspace root, injecting one
// --override-input flag for each workspace dep of the terminal flake as
// recorded in the workspace lock. Uses per-consumer lock edges for aliasing.
//
// nix's stdout and stderr are streamed live to out (matching Build/FlakeCheck)
// rather than being buffered and truncated into the returned error — callers
// such as the pre-commit test hooks rely on seeing the full build/test output.
//
// Subcommands in denyListedNixSubcommands are refused with a clear error.
func (ws *Workspace) NixCommand(ctx context.Context, out io.Writer, args []string) error {
	// The CLI passes through any "--" separator (cobra DisableFlagParsing
	// mode); strip a leading "--" so the deny-list and the eventual nix
	// invocation see the bare subcommand.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if denied, match := matchesDeniedSubcommand(args); denied {
		return fmt.Errorf("nix %s is incompatible with workspace overrides; refused", strings.Join(match, " "))
	}
	terminal, err := ws.effectiveTerminal("")
	if err != nil {
		return fmt.Errorf("resolve terminal: %w", err)
	}
	overrides := ws.overrideInputArgsFor(terminal, overrideOpts{})
	full := append([]string{}, args...)
	full = append(full, overrides...)
	_, err = ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root, Stdout: out, Stderr: out})
	return err
}

// nixValueFlagArity lists the nix flags that consume following tokens as VALUES,
// mapped to how many they consume. commandTokens skips those value tokens so a
// value cannot land between two deny-list words and break the resolved
// subcommand path's contiguity (bead pg2-9p527): e.g.
// `flake --option build-cores 4 update`, which nix parses as `flake update`
// with a global option, must still be recognized as `flake update`.
//
// This is a best-effort table for a GUARD-RAIL, not a security boundary: nix
// exposes many settings as `--<setting> VALUE`, so an unlisted value-taking flag
// whose (non-dash) value is wedged between deny words could still slip a
// lock-mutating subcommand past the guard. Users can always run `nix` directly,
// the documented escape hatch. The `--flag=value` form is a single token and
// needs no entry here.
var nixValueFlagArity = map[string]int{
	// Arity 2: NAME VALUE (or NAME EXPR).
	"--option":         2,
	"--arg":            2,
	"--argstr":         2,
	"--override-input": 2,
	"--override-flake": 2,
	// Arity 1: common global and flake value-taking flags / settings.
	"--log-format":               1,
	"--max-jobs":                 1,
	"-j":                         1,
	"--cores":                    1,
	"--builders":                 1,
	"--store":                    1,
	"--eval-store":               1,
	"--include":                  1,
	"-I":                         1,
	"--file":                     1,
	"-f":                         1,
	"--expr":                     1,
	"--out-link":                 1,
	"-o":                         1,
	"--profile":                  1,
	"--inputs-from":              1,
	"--update-input":             1,
	"--reference-lock-file":      1,
	"--output-lock-file":         1,
	"--commit-lock-file-summary": 1,
}

// commandTokens resolves the nix subcommand path from raw argv by dropping
// option-like tokens (any beginning with "-", incl. a lone "--" and short flags)
// AND the value tokens consumed by value-taking flags (see nixValueFlagArity).
// Nix subcommand words and installables never begin with "-", so what remains is
// the subcommand path plus any trailing positionals — with deny-list words kept
// CONTIGUOUS even when a value-taking global flag sits between them (bead
// pg2-9p527, extending pg2-odu4p).
func commandTokens(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			// Also skip this flag's value tokens (arity 0 for unknown/boolean
			// flags via the map's zero value) so they never survive as
			// positionals that break deny-sequence contiguity.
			i += nixValueFlagArity[a]
			continue
		}
		out = append(out, a)
	}
	return out
}

// matchesDeniedSubcommand reports whether the resolved nix subcommand path
// (args with option-like tokens removed) contains any denyListedNixSubcommands
// sequence as a contiguous run. Matching the de-flagged path — rather than the
// raw argv from index 0 — makes the guard insensitive to leading, value-taking,
// and interspersed global flags (e.g. `--verbose flake update`,
// `--option build-cores 4 flake update`). Trailing args after a match are
// tolerated (e.g. `flake update --commit-lock-file` still matches).
func matchesDeniedSubcommand(args []string) (bool, []string) {
	cmd := commandTokens(args)
	for _, deny := range denyListedNixSubcommands {
		for i := 0; i+len(deny) <= len(cmd); i++ {
			match := true
			for j := range deny {
				if cmd[i+j] != deny[j] {
					match = false
					break
				}
			}
			if match {
				return true, deny
			}
		}
	}
	return false, nil
}
