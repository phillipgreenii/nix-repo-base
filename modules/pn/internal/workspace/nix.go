package workspace

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// nonOverrideAction selects what NixCommand does when the nix subcommand is one
// for which --override-input does not apply (currently `flake update` and
// `flake lock`). Mirrors pn-ws-nix's --non-override-subcommand-action.
type nonOverrideAction int

const (
	// nonOverrideWarn prints a message to stderr, then runs nix without
	// overrides. This is the default.
	nonOverrideWarn nonOverrideAction = iota
	// nonOverrideError aborts with an error and does not run nix.
	nonOverrideError
	// nonOverrideIgnore runs nix without overrides, silently.
	nonOverrideIgnore
)

// nonOverrideActionEnv is the env var that selects the default action when the
// --non-override-subcommand-action flag is absent.
const nonOverrideActionEnv = "PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION"

// NixCommand runs `nix <args>` from the workspace root, injecting one
// --override-input flag for each repo in the workspace so the local clone is
// used in place of the upstream input. Override flags are emitted in
// alphabetical order by repo key for deterministic command construction.
//
// Overrides are NOT applied to subcommands that reject them (`flake update`,
// `flake lock`); those are handled per the resolved non-override action
// (--non-override-subcommand-action flag > PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION
// env > warn). A `--` end-of-options separator is refused, because the wrapper
// cannot safely append overrides past it (they would reach the user's program,
// not nix).
func (ws *Workspace) NixCommand(ctx context.Context, args []string) error {
	action, rest, err := parseNonOverrideAction(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("nix requires at least one argument")
	}

	d := decideNix(rest, action)
	if d.abort != "" {
		return fmt.Errorf("%s", d.abort)
	}
	if d.warn != "" {
		fmt.Fprintln(os.Stderr, d.warn)
	}

	full := append([]string{}, rest...)
	if d.injectOverrides {
		full = append(full, ws.overrideInputArgs(overrideOpts{})...)
	}
	_, err = ws.runner.Run(ctx, "nix", full, exec.RunOptions{Dir: ws.root})
	return err
}

// parseNonOverrideAction strips a leading --non-override-subcommand-action
// flag (space- or equals-separated) from args and resolves the action:
// flag value > env var > "warn" default. Returns the resolved action and the
// remaining args (the actual nix invocation). An unrecognized value is an error.
func parseNonOverrideAction(args []string) (nonOverrideAction, []string, error) {
	const flag = "--non-override-subcommand-action"
	value := ""
	found := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == flag:
			if i+1 >= len(args) {
				return 0, nil, fmt.Errorf("%s requires a value (error, warn, or ignore)", flag)
			}
			value, found = args[i+1], true
			i++
		case len(arg) > len(flag)+1 && arg[:len(flag)+1] == flag+"=":
			value, found = arg[len(flag)+1:], true
		default:
			rest = append(rest, arg)
		}
	}
	if !found {
		value = os.Getenv(nonOverrideActionEnv)
	}
	switch value {
	case "", "warn":
		return nonOverrideWarn, rest, nil
	case "error":
		return nonOverrideError, rest, nil
	case "ignore":
		return nonOverrideIgnore, rest, nil
	default:
		return 0, nil, fmt.Errorf("invalid %s value: %q (allowed: error, warn, ignore)", flag, value)
	}
}

// nixDecision is the outcome of inspecting a nix invocation: whether to inject
// overrides, an optional stderr warning to print first, and an optional abort
// message (when set, nix is not run and the message becomes an error).
type nixDecision struct {
	injectOverrides bool
	warn            string
	abort           string
}

// decideNix decides how to run the nix invocation `rest`. Deny-listed
// subcommands (where --override-input is silently ignored) run without
// overrides per action; a `--` separator on an override-applicable subcommand
// is refused; everything else gets overrides.
func decideNix(rest []string, action nonOverrideAction) nixDecision {
	sub := detectNixSubcommand(rest)
	if isOverrideDenied(sub) {
		switch action {
		case nonOverrideError:
			return nixDecision{abort: fmt.Sprintf(
				"overrides not applicable to %q; run `nix %s` directly if intentional",
				sub, strings.Join(rest, " "))}
		case nonOverrideWarn:
			return nixDecision{warn: fmt.Sprintf(
				"pn workspace nix: overrides not applicable to %q. Running nix without overrides; use bare `nix` directly to silence this.",
				sub)}
		default: // nonOverrideIgnore
			return nixDecision{}
		}
	}
	// --override-input cannot be safely appended past a `--`: it would be handed
	// to the user's program (nix run/shell), not to nix itself.
	if slices.Contains(rest, "--") {
		return nixDecision{abort: "cannot inject overrides when '--' is present in nix args; drop the '--' or run bare nix with --override-input manually"}
	}
	return nixDecision{injectOverrides: true}
}

// isOverrideDenied reports whether subcommand is one where --override-input is
// silently ignored by nix (so the wrapper must not inject overrides).
func isOverrideDenied(subcommand string) bool {
	switch subcommand {
	case "flake update", "flake lock":
		return true
	default:
		return false
	}
}

// detectNixSubcommand returns the nix subcommand from args, skipping leading
// global flags (anything starting with "-"). When the first positional is
// "flake", it combines with the next positional (also skipping flags) so that
// "flake update" / "flake lock" can be matched. Returns "" if there is no
// positional argument. Flag arity is not parsed (matching the bash).
func detectNixSubcommand(args []string) string {
	sub := ""
	idx := 0
	for idx < len(args) {
		if strings.HasPrefix(args[idx], "-") {
			idx++
			continue
		}
		sub = args[idx]
		break
	}
	if sub != "flake" {
		return sub
	}
	for peek := idx + 1; peek < len(args); peek++ {
		if !strings.HasPrefix(args[peek], "-") {
			return "flake " + args[peek]
		}
	}
	return sub
}
