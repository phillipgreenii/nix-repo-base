package exec

import (
	"path/filepath"
	"strings"
)

// SubcommandOther is the pn.exec.subcommand value of every command that is not
// in the closed allowlist below. A closed set keeps the attribute low
// cardinality: nothing from a user's argv ever becomes a value.
const SubcommandOther = "other"

// execSubcommands is the closed allowlist of pn.exec.subcommand values: the
// normalized tool name plus, for the multi-verb tools, the verb (and for nix
// the subcommand path). It exists so a trace tells `darwin-rebuild build` from
// `sudo darwin-rebuild switch`, which pn.exec's process.executable.name cannot
// ("sudo" for both).
var execSubcommands = map[string]bool{
	"darwin-rebuild build":    true,
	"darwin-rebuild switch":   true,
	"darwin-rebuild activate": true,
	"darwin-rebuild check":    true,
	"nixos-rebuild build":     true,
	"nixos-rebuild switch":    true,
	"nixos-rebuild test":      true,
	"nixos-rebuild boot":      true,
	"nix-env":                 true,
	"nix-store":               true,
	"nix build":               true,
	"nix eval":                true,
	"nix fmt":                 true,
	"nix run":                 true,
	"nix path-info":           true,
	"nix flake check":         true,
	"nix flake update":        true,
	"nix flake lock":          true,
	"nix flake metadata":      true,
	"nix store gc":            true,
}

// rebuildValueFlagArity are the value-taking flags of darwin-rebuild and
// nixos-rebuild that the nix table does not carry; the verb is the first token
// that is neither a flag nor a flag's value.
var rebuildValueFlagArity = map[string]int{"--flake": 1, "--profile-name": 1, "-p": 1}

// ExecSubcommand normalizes a command's argv to its pn.exec.subcommand value,
// from the closed allowlist or SubcommandOther. It looks through the wrappers
// pn puts in front of a command: a leading `sudo` (and its dash options), then
// `env` with its `-i` / `--ignore-environment` option and any NAME=VALUE tokens
// (apply's root steps are `sudo env -i HOME=... NIX_REMOTE=daemon PATH=...
// <nix-env>` and `sudo <out>/sw/bin/darwin-rebuild activate`), and takes the
// basename of an absolute argv[0]. Flag values are skipped with the same table
// the nix subcommand deny-list uses (NixValueFlagArity).
func ExecSubcommand(name string, args []string) string {
	argv := make([]string, 0, 1+len(args))
	argv = append(argv, name)
	argv = append(argv, args...)

	i := 0
	if filepath.Base(argv[i]) == "sudo" {
		i++
		for i < len(argv) && strings.HasPrefix(argv[i], "-") {
			i++
		}
	}
	if i < len(argv) && filepath.Base(argv[i]) == "env" {
		i++
		for i < len(argv) && (argv[i] == "-i" || argv[i] == "--ignore-environment" || isEnvAssignment(argv[i])) {
			i++
		}
	}
	if i >= len(argv) {
		return SubcommandOther
	}
	tool, rest := filepath.Base(argv[i]), argv[i+1:]

	var key string
	switch tool {
	case "nix-env", "nix-store":
		key = tool
	case "darwin-rebuild", "nixos-rebuild":
		if toks := subcommandTokens(rest, rebuildValueFlagArity); len(toks) > 0 {
			key = tool + " " + toks[0]
		}
	case "nix":
		toks := subcommandTokens(rest, nil)
		for n := min(len(toks), 3); n > 0; n-- {
			if cand := "nix " + strings.Join(toks[:n], " "); execSubcommands[cand] {
				key = cand
				break
			}
		}
	}
	if execSubcommands[key] {
		return key
	}
	return SubcommandOther
}

// subcommandTokens drops option-like tokens and the values of value-taking
// flags (NixValueFlagArity plus extra), leaving the subcommand path and any
// trailing positionals.
func subcommandTokens(args []string, extra map[string]int) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if n, ok := extra[a]; ok {
				i += n
			} else {
				i += NixValueFlagArity[a]
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// isEnvAssignment reports whether tok has the NAME=VALUE form env accepts: a
// non-empty name of letters, digits and underscores not starting with a digit.
func isEnvAssignment(tok string) bool {
	name, _, ok := strings.Cut(tok, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
