package exec

// NixValueFlagArity lists the nix flags that consume following tokens as VALUES,
// mapped to how many they consume. workspace.commandTokens skips those value tokens so a
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
var NixValueFlagArity = map[string]int{
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
