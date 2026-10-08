// Package telemetrycfg resolves pn's OpenTelemetry configuration (ADR 0028):
// whether telemetry is on, which OTLP endpoint it uses, which wrapper binary
// pn may use for nix, and the small user-facing helpers around it (the trace
// hint, the collector probe, the sudo-wrapper path guard).
//
// It deliberately has NO OpenTelemetry dependency: it only decides WHETHER and
// WHERE to export. The span/metric machinery lives in the telemetry facade.
// Keeping the decision separate makes the no-telemetry path trivially free of
// side effects (the Null Object rule: with no endpoint, nothing is created, no
// goroutine is started, no connection is opened and no file is written).
//
// Precedence, first match wins (the file is read by non-root only):
//
//  1. forced off, in any case: --no-telemetry, PG_NIX_LOG_DISABLE=1,
//     OTEL_SDK_DISABLED=true;
//  2. --otlp-endpoint flag (an explicit per-run opt-in: beats the file's
//     enabled = false);
//  3. telemetry.toml `enabled = false` (the per-system runtime switch: off,
//     even when OTEL_EXPORTER_OTLP_ENDPOINT or the file's endpoint is set);
//  4. OTEL_EXPORTER_OTLP_ENDPOINT;
//  5. telemetry.toml `endpoint`.
//
// An absent `enabled` key means "on iff an endpoint resolves" (the original
// rule); `enabled = true` without any endpoint resolves to off (there is no
// default endpoint) and the doctor reports it.
package telemetrycfg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Environment variable names read by the resolver.
const (
	EnvEndpoint      = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvSDKDisabled   = "OTEL_SDK_DISABLED"
	EnvWrapDisable   = "PG_NIX_LOG_DISABLE"
	EnvTraceHint     = "PN_TRACE_HINT"
	envXDGConfigHome = "XDG_CONFIG_HOME"
	envHome          = "HOME"
)

// Source names where the resolved endpoint came from.
const (
	SourceNone = ""
	SourceFlag = "flag"
	SourceEnv  = "env"
	SourceFile = "file"
)

// FileConfig is the parsed ~/.config/pn/telemetry.toml. The file is generated
// by the home-manager module (phillipgreenii.pn.telemetry.*); every key is
// optional. Enabled is a pointer so "absent" (derive from the endpoint) is
// distinguishable from an explicit false.
type FileConfig struct {
	Enabled     *bool  `toml:"enabled"`
	Endpoint    string `toml:"endpoint"`
	WrapperPath string `toml:"wrapper_path"`
}

// FilePath returns the telemetry.toml path: ${XDG_CONFIG_HOME:-$HOME/.config}/pn/telemetry.toml.
func FilePath(getenv func(string) string) string {
	base := getenv(envXDGConfigHome)
	if base == "" {
		home := getenv(envHome)
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "pn", "telemetry.toml")
}

// LoadFile reads and parses path. A missing file, an empty path, or malformed
// TOML all yield the zero FileConfig and (for malformed TOML only) a non-nil
// error, so callers can choose to ignore it: a broken optional config file
// MUST NOT stop pn.
func LoadFile(path string) (FileConfig, error) {
	var c FileConfig
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return FileConfig{}, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return FileConfig{}, err
	}
	return c, nil
}

// Flags are the values of pn's telemetry-related command-line flags.
type Flags struct {
	NoTelemetry bool   // --no-telemetry
	Endpoint    string // --otlp-endpoint URL
	Verbose     bool   // -v / --verbose
}

// Resolution is the outcome of Resolve.
type Resolution struct {
	// Enabled is true iff an endpoint resolved and nothing forced telemetry off
	// (including telemetry.toml `enabled = false`).
	Enabled bool
	// Endpoint is the OTLP endpoint; empty when !Enabled.
	Endpoint string
	// Source is SourceFlag, SourceEnv or SourceFile when Enabled.
	Source string
	// WrapperPath is the configured pg-nix-log-wrapped path from telemetry.toml
	// (empty if none or if the file was not read). It is only a candidate: pn
	// MUST apply ValidateSudoWrapper before running it as root.
	WrapperPath string
	// Forced names the control that forced telemetry off ("" if none). It is
	// ForcedByFile when telemetry.toml says `enabled = false`, and
	// ForcedUnreachable when the collector probe failed (Prepare).
	Forced string
	// FileEnabled is telemetry.toml's `enabled` key (nil if absent or the file
	// was not read).
	FileEnabled *bool
	// ProbeErr is the collector-probe failure that turned telemetry off for
	// the run (Prepare); ProbeEndpoint and ProbeSource name what was probed.
	// The doctor uses them to report a configured-on but unreachable collector,
	// which Enabled=false alone would hide.
	ProbeErr      error
	ProbeEndpoint string
	ProbeSource   string
	// Verbose mirrors Flags.Verbose.
	Verbose bool
	// FileErr is a non-fatal telemetry.toml parse/read error, for -v diagnostics.
	FileErr error
}

// Forced reasons that are not a flag or an environment variable.
const (
	ForcedByFile      = "telemetry.toml enabled = false"
	ForcedUnreachable = "collector unreachable"
)

// Inputs are the dependencies of Resolve, injectable for tests.
type Inputs struct {
	Flags  Flags
	Getenv func(string) string
	// IsRoot reports whether the process runs as uid 0. A root process never
	// reads the user-owned telemetry.toml (a user-writable file MUST NOT steer
	// root behaviour).
	IsRoot bool
}

// Resolve applies the precedence and force-off rules. It performs no network
// call and creates no file; the only side effect is reading telemetry.toml
// when no flag or env endpoint decided the question.
func Resolve(in Inputs) Resolution {
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	res := Resolution{Verbose: in.Flags.Verbose}

	// The wrapper path is only ever sourced from the file; read it whenever
	// the file is readable so the doctor can report on it even when the
	// endpoint came from the environment.
	var file FileConfig
	if !in.IsRoot {
		var err error
		file, err = LoadFile(FilePath(getenv))
		res.FileErr = err
	}
	res.WrapperPath = file.WrapperPath
	res.FileEnabled = file.Enabled

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

	if in.Flags.Endpoint == "" && file.Enabled != nil && !*file.Enabled {
		res.Forced = ForcedByFile
		return res
	}

	switch {
	case in.Flags.Endpoint != "":
		res.Endpoint, res.Source = in.Flags.Endpoint, SourceFlag
	case getenv(EnvEndpoint) != "":
		res.Endpoint, res.Source = getenv(EnvEndpoint), SourceEnv
	case file.Endpoint != "":
		res.Endpoint, res.Source = file.Endpoint, SourceFile
	}
	res.Enabled = res.Endpoint != ""
	return res
}

// Prepare is the entry-point helper: it scans args for the telemetry flags,
// resolves the configuration and, whenever telemetry is enabled, probes the
// collector with one bounded TCP connect (pg2-aoza4). An unreachable collector
// turns telemetry off for the run, so pn never tries to write to a machine
// that has no local OTel suite running; the one-line
// `telemetry disabled: collector unreachable (...)` note goes to stderr only
// under -v, so the message (when printed) is true and a default run stays
// silent. The disabled path (no endpoint, or forced off) never probes.
func Prepare(ctx context.Context, args []string, getenv func(string) string, isRoot bool, stderr io.Writer) Resolution {
	res := Resolve(Inputs{Flags: ScanArgs(args), Getenv: getenv, IsRoot: isRoot})
	if res.Enabled {
		if err := Probe(ctx, res.Endpoint, ProbeTimeout); err != nil {
			if res.Verbose {
				_, _ = fmt.Fprintln(stderr, UnreachableMessage(res.Endpoint))
			}
			res.ProbeErr, res.ProbeEndpoint, res.ProbeSource = err, res.Endpoint, res.Source
			res.Enabled = false
			res.Endpoint = ""
			res.Source = SourceNone
			res.Forced = ForcedUnreachable
		}
	}
	return res
}
