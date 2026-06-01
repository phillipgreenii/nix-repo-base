package workspace

import (
	"context"
	"fmt"
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
// Matched as a `<subcommand>` prefix of args: e.g. {"flake", "update", ...}
// matches the "flake update" entry regardless of trailing args/flags.
var denyListedNixSubcommands = [][]string{
	{"flake", "update"},
	{"flake", "lock"},
}

// NixCommand runs `nix <args>` from the workspace root, injecting one
// --override-input flag for each non-terminal workspace repo that the
// terminal flake consumes, pinning that input to its local clone.
//
// Subcommands in denyListedNixSubcommands are refused with a clear error.
func (ws *Workspace) NixCommand(ctx context.Context, args []string) error {
	// The CLI passes through any "--" separator (cobra DisableFlagParsing
	// mode); strip a leading "--" so the deny-list and the eventual nix
	// invocation see the bare subcommand.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if denied, match := matchesDeniedSubcommand(args); denied {
		return fmt.Errorf("nix %s is incompatible with workspace overrides; refused", strings.Join(match, " "))
	}
	repos, err := ws.Discover()
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	overrides := computeOverrideArgsFromRepos(repos)
	full := append([]string{}, args...)
	full = append(full, overrides...)
	_, err = ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root})
	return err
}

// matchesDeniedSubcommand returns (true, matched) if args begins with any
// of the denyListedNixSubcommands prefixes. Comparison is positional and
// exact for the prefix; extra args after the matched prefix are tolerated
// (e.g. `nix flake update --commit-lock-file` still matches "flake update").
func matchesDeniedSubcommand(args []string) (bool, []string) {
	for _, deny := range denyListedNixSubcommands {
		if len(args) < len(deny) {
			continue
		}
		match := true
		for i := range deny {
			if args[i] != deny[i] {
				match = false
				break
			}
		}
		if match {
			return true, deny
		}
	}
	return false, nil
}
