package telemetrycfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Spool-mode sources: where the spool directory came from.
const (
	SpoolSourceFlag    = "flag"
	SpoolSourceFile    = "file"
	SpoolSourceDefault = "default"
)

// NoticePrefix starts every deprecation notice pn prints about the retired
// endpoint-based configuration.
const NoticePrefix = "pn: telemetry: "

// SpoolResolution is the outcome of ResolveSpool.
type SpoolResolution struct {
	// Enabled is true iff spool telemetry is on for this run.
	Enabled bool
	// Dir is the absolute spool directory; empty when !Enabled.
	Dir string
	// Source is a SpoolSource* constant when Enabled.
	Source string
	// Forced names the control that forced telemetry off ("" if none).
	Forced string
	// Err reports an unusable spool directory (relative path, or no
	// resolvable default). Telemetry is off whenever Err is set.
	Err error
}

// ResolveSpool applies the spool-mode precedence, first match wins:
//
//  1. forced off in any case: --no-telemetry, PG_NIX_LOG_DISABLE=1,
//     OTEL_SDK_DISABLED=true;
//  2. --otel-spool-dir flag (an explicit per-run opt-in: beats the file's
//     enabled = false);
//  3. telemetry.toml `enabled = false` (off, whatever else is set);
//  4. telemetry.toml `enabled = true` (on; spool_dir, else the default
//     ${XDG_STATE_HOME:-$HOME/.local/state}/otel-spool);
//  5. otherwise off (an absent `enabled` key is off: there is no endpoint to
//     infer from).
//
// A root process never reads the user file: it is on only when the flag
// enables it, and never derives a directory under a user's home. The spool
// directory MUST be absolute. ResolveSpool performs no I/O besides reading
// telemetry.toml (non-root only).
func ResolveSpool(in Inputs) SpoolResolution {
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	var res SpoolResolution
	switch {
	case in.Flags.NoTelemetry:
		res.Forced = "--no-telemetry"
	case getenv(EnvWrapDisable) == "1":
		res.Forced = EnvWrapDisable + "=1"
	case strings.EqualFold(getenv(EnvSDKDisabled), "true"):
		res.Forced = EnvSDKDisabled + "=true"
	}
	if res.Forced != "" {
		return res
	}

	if in.Flags.SpoolDir != "" {
		return spoolDirOrErr(res, in.Flags.SpoolDir, SpoolSourceFlag)
	}
	if in.IsRoot {
		return res
	}
	file, _ := LoadFile(FilePath(getenv))
	if file.Enabled == nil {
		return res
	}
	if !*file.Enabled {
		res.Forced = ForcedByFile
		return res
	}
	if file.SpoolDir != "" {
		return spoolDirOrErr(res, file.SpoolDir, SpoolSourceFile)
	}
	dir := DefaultSpoolDir(getenv)
	if dir == "" {
		res.Err = fmt.Errorf("no default spool directory: neither XDG_STATE_HOME nor HOME is set")
		return res
	}
	return spoolDirOrErr(res, dir, SpoolSourceDefault)
}

func spoolDirOrErr(res SpoolResolution, dir, source string) SpoolResolution {
	if !filepath.IsAbs(dir) {
		res.Err = fmt.Errorf("spool directory %q is not absolute", dir)
		return res
	}
	res.Enabled, res.Dir, res.Source = true, filepath.Clean(dir), source
	return res
}

// DefaultSpoolDir returns ${XDG_STATE_HOME:-$HOME/.local/state}/otel-spool, or
// "" when neither variable is set.
func DefaultSpoolDir(getenv func(string) string) string {
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		home := getenv(envHome)
		if home == "" {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "otel-spool")
}

// NoticeLeftoverEndpointKey is printed when telemetry.toml still carries the
// retired endpoint key.
func NoticeLeftoverEndpointKey() string {
	return NoticePrefix + "telemetry.toml key \"endpoint\" is deprecated and ignored; telemetry now writes to a file spool (set spool_dir or --otel-spool-dir)"
}

// NoticeEnvEndpoint is printed when OTEL_EXPORTER_OTLP_ENDPOINT is set.
func NoticeEnvEndpoint() string {
	return NoticePrefix + EnvEndpoint + " is deprecated and ignored; telemetry now writes to a file spool (set spool_dir or --otel-spool-dir)"
}

// NoticeFlagEndpoint is printed when --otlp-endpoint is given.
func NoticeFlagEndpoint() string {
	return NoticePrefix + "--otlp-endpoint is deprecated and ignored; use --otel-spool-dir"
}

// DeprecationNotices returns the notice lines that apply to this run, in a
// fixed order (flag, environment, file key); empty when none apply. fileEndpoint
// is the endpoint value read from telemetry.toml ("" if absent).
func DeprecationNotices(flags Flags, getenv func(string) string, fileEndpoint string) []string {
	var out []string
	if flags.Endpoint != "" {
		out = append(out, NoticeFlagEndpoint())
	}
	if getenv != nil && getenv(EnvEndpoint) != "" {
		out = append(out, NoticeEnvEndpoint())
	}
	if fileEndpoint != "" {
		out = append(out, NoticeLeftoverEndpointKey())
	}
	return out
}
