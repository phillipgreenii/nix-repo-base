package telemetrycfg

import "strings"

// ScanArgs extracts the telemetry-related global flags from a pn argv (without
// the program name) BEFORE cobra parses it. Telemetry has to be created before
// the command tree runs (so even a failing parse is covered by one Shutdown),
// which is earlier than cobra can hand us flag values.
//
// Recognised: --no-telemetry, --otlp-endpoint URL / --otlp-endpoint=URL,
// -v / --verbose. Scanning stops at a bare "--", and at the `nix` verb of
// `pn workspace nix`: that verb disables flag parsing and forwards everything
// after it to nix verbatim, where `-v` is nix's own flag. The cobra flags of
// the same names exist (so --help lists them and cobra does not reject them);
// this scanner is the single source of the VALUES.
func ScanArgs(args []string) Flags {
	var f Flags
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return f
		case a == "nix" && i > 0 && args[i-1] == "workspace":
			return f
		case a == "--no-telemetry":
			f.NoTelemetry = true
		case a == "-v" || a == "--verbose":
			f.Verbose = true
		case a == "--otlp-endpoint":
			if i+1 < len(args) {
				f.Endpoint = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--otlp-endpoint="):
			f.Endpoint = strings.TrimPrefix(a, "--otlp-endpoint=")
		}
	}
	return f
}
