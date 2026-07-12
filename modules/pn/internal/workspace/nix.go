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
// Matched against the de-flagged command tokens (option-like tokens removed) as
// a contiguous subsequence anywhere: e.g. {"flake", "update"} matches
// `flake update`, `--verbose flake update`, `--option build-cores 4 flake
// update`, and `flake --verbose update` alike (bead pg2-odu4p).
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

// commandTokens strips option-like tokens (any beginning with "-", incl. a lone
// "--", short flags like -v, and value-taking flags like --option) so the
// deny-list is matched against the resolved nix subcommand path rather than the
// raw argv. Nix subcommand words and installables never begin with "-"; value
// words of value-taking flags survive as harmless positionals that cannot equal
// an adjacent deny sequence.
func commandTokens(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
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
