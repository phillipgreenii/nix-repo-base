package app

// Operation label for the nix.invocation span and the nix.invocation.duration
// metric (bead pg2-sdbhh4.3).
//
// The "command" attribute is only the base name (nix, darwin-rebuild), so
// error rates could not be split by operation. Subcommand derives a BOUNDED
// operation name from CMD's argv: only values from the two allowlists below
// or "other" can ever be returned, never a raw user string, so it is safe as
// a metric label.

import (
	"path/filepath"
)

// SubcommandOther is the label of every operation outside the allowlists
// (unknown verb, no verb, or a command that is neither nix nor darwin-rebuild).
const SubcommandOther = "other"

// subcommandValueFlagArity lists the flags that consume following tokens as
// VALUES, mapped to how many they consume, so a flag value that happens to
// equal a verb (`nix --option foo build eval .#x`) is never taken as the verb.
// Copied from pn's nixValueFlagArity (modules/pn/internal/workspace/nix.go;
// data copied, not imported across modules) plus the flags the wrapped
// commands take (--extra-experimental-features, --experimental-features,
// --flake, --profile-name, -p, -G). The `--flag=value` form is one token and
// needs no entry. A best-effort table: an unlisted value-taking flag whose
// value equals a verb still misleads the scan (see TestSubcommand).
var subcommandValueFlagArity = map[string]int{
	// Arity 2: NAME VALUE (or NAME EXPR).
	"--option":         2,
	"--arg":            2,
	"--argstr":         2,
	"--override-input": 2,
	"--override-flake": 2,
	// Arity 1.
	"--extra-experimental-features": 1,
	"--experimental-features":       1,
	"--log-format":                  1,
	"--max-jobs":                    1,
	"-j":                            1,
	"--cores":                       1,
	"--builders":                    1,
	"--store":                       1,
	"--eval-store":                  1,
	"--include":                     1,
	"-I":                            1,
	"--file":                        1,
	"-f":                            1,
	"--expr":                        1,
	"--out-link":                    1,
	"-o":                            1,
	"--profile":                     1,
	"--profile-name":                1,
	"-p":                            1,
	"--flake":                       1,
	"-G":                            1,
	"--inputs-from":                 1,
	"--update-input":                1,
	"--reference-lock-file":         1,
	"--output-lock-file":            1,
	"--commit-lock-file-summary":    1,
}

// nixVerbs are the allowlisted `nix` subcommands; "flake" needs an allowlisted
// second word (nixFlakeVerbs) and is then reported as "flake <word>".
var nixVerbs = map[string]bool{
	"build": true, "eval": true, "fmt": true, "run": true, "develop": true,
	"shell": true, "path-info": true, "store": true, "copy": true, "flake": true,
}

var nixFlakeVerbs = map[string]bool{
	"check": true, "update": true, "lock": true, "show": true, "metadata": true,
}

// darwinRebuildVerbs are the allowlisted nix-darwin actions.
var darwinRebuildVerbs = map[string]bool{
	"switch": true, "activate": true, "build": true, "check": true,
	"edit": true, "changelog": true,
}

// Subcommand returns the bounded operation label of cmd (CMD and its
// arguments): the allowlisted verb ("build", "flake check", "switch", ...) or
// SubcommandOther. Flags and the values of known value-taking flags are
// skipped; the first remaining token found in the allowlist for cmd[0]'s base
// name wins; scanning stops at "--" (what follows belongs to the program being
// run).
func Subcommand(cmd []string) string {
	if len(cmd) == 0 {
		return SubcommandOther
	}
	var verbs map[string]bool
	switch filepath.Base(cmd[0]) {
	case "nix":
		verbs = nixVerbs
	case "darwin-rebuild":
		verbs = darwinRebuildVerbs
	default:
		return SubcommandOther
	}
	var words []string
scan:
	for i := 1; i < len(cmd); i++ {
		a := cmd[i]
		switch {
		case a == "--":
			break scan
		case len(a) > 0 && a[0] == '-':
			i += subcommandValueFlagArity[a]
		default:
			words = append(words, a)
		}
	}
	for i, w := range words {
		if !verbs[w] {
			continue
		}
		if w != "flake" {
			return w
		}
		if i+1 < len(words) && nixFlakeVerbs[words[i+1]] {
			return "flake " + words[i+1]
		}
		return SubcommandOther
	}
	return SubcommandOther
}
