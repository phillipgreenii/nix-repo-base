// Package telemetrycfg resolves pn's OpenTelemetry configuration (ADR 0028):
// whether telemetry is on, which spool directory it writes to, which wrapper
// binary pn may use for nix, and the small user-facing helpers around it (the
// trace hint, the deprecation notices, the sudo-wrapper path guard).
//
// It deliberately has NO OpenTelemetry dependency: it only decides WHETHER and
// WHERE to export. The span/metric machinery lives in the telemetry facade.
// Keeping the decision separate makes the no-telemetry path trivially free of
// side effects (the Null Object rule: when off, nothing is created, no
// goroutine is started and no file is written).
//
// The precedence is documented on ResolveSpool. The retired endpoint
// configuration (--otlp-endpoint, OTEL_EXPORTER_OTLP_ENDPOINT, the file's
// endpoint key) no longer enables telemetry; each prints a deprecation notice.
package telemetrycfg

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Environment variable names read by the resolver.
const (
	EnvEndpoint      = "OTEL_EXPORTER_OTLP_ENDPOINT" // deprecated; only triggers a notice
	EnvSDKDisabled   = "OTEL_SDK_DISABLED"
	EnvWrapDisable   = "PG_NIX_LOG_DISABLE"
	EnvTraceHint     = "PN_TRACE_HINT"
	envXDGConfigHome = "XDG_CONFIG_HOME"
	envHome          = "HOME"
)

// Source names where the resolved spool directory came from.
const (
	SourceNone = ""
	SourceFlag = SpoolSourceFlag
	SourceFile = SpoolSourceFile
	// SourceDefault is the ${XDG_STATE_HOME:-$HOME/.local/state}/otel-spool
	// default, used when telemetry.toml says enabled = true with no spool_dir.
	SourceDefault = SpoolSourceDefault
)

// FileConfig is the parsed ~/.config/pn/telemetry.toml. The file is generated
// by the home-manager module (phillipgreenii.pn.telemetry.*); every key is
// optional. Enabled is a pointer so "absent" (off) is
// distinguishable from an explicit false.
type FileConfig struct {
	Enabled     *bool  `toml:"enabled"`
	Endpoint    string `toml:"endpoint"`
	WrapperPath string `toml:"wrapper_path"`
	// SpoolDir is the optional spool directory (must be absolute).
	SpoolDir string `toml:"spool_dir"`
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
	SpoolDir    string // --otel-spool-dir DIR
	Verbose     bool   // -v / --verbose
}

// Resolution is the outcome of Resolve.
type Resolution struct {
	// Enabled is true iff a usable spool directory resolved and nothing forced
	// telemetry off (including telemetry.toml `enabled = false`).
	Enabled bool
	// SpoolDir is the absolute spool directory; empty when !Enabled.
	SpoolDir string
	// Source is SourceFlag, SourceFile or SourceDefault when Enabled.
	Source string
	// WrapperPath is the configured pg-nix-log-wrapped path from telemetry.toml
	// (empty if none or if the file was not read). It is only a candidate: pn
	// MUST apply ValidateSudoWrapper before running it as root.
	WrapperPath string
	// Forced names the control that forced telemetry off ("" if none). It is
	// ForcedByFile when telemetry.toml says `enabled = false`.
	Forced string
	// FileEnabled is telemetry.toml's `enabled` key (nil if absent or the file
	// was not read).
	FileEnabled *bool
	// SpoolErr is an unusable spool directory (relative path, or no resolvable
	// default). Telemetry is off whenever it is set; the doctor reports it.
	SpoolErr error
	// Notices are the deprecation lines that apply to this run (see
	// DeprecationNotices); Prepare prints them.
	Notices []string
	// Verbose mirrors Flags.Verbose.
	Verbose bool
	// FileErr is a non-fatal telemetry.toml parse/read error, for -v diagnostics.
	FileErr error
}

// ForcedByFile is the Forced reason for telemetry.toml `enabled = false`.
const ForcedByFile = "telemetry.toml enabled = false"

// Inputs are the dependencies of Resolve, injectable for tests.
type Inputs struct {
	Flags  Flags
	Getenv func(string) string
	// IsRoot reports whether the process runs as uid 0. A root process never
	// reads the user-owned telemetry.toml (a user-writable file MUST NOT steer
	// root behaviour).
	IsRoot bool
}

// Resolve applies the spool-mode precedence (ResolveSpool) and gathers the
// wrapper path and deprecation notices. It opens no connection and creates no
// file; the only side effect is reading telemetry.toml (non-root only).
func Resolve(in Inputs) Resolution {
	getenv := in.Getenv
	if getenv == nil {
		getenv = os.Getenv
		in.Getenv = getenv
	}
	res := Resolution{Verbose: in.Flags.Verbose}

	// The wrapper path is only ever sourced from the file; read it whenever
	// the file is readable so the doctor can report on it.
	var file FileConfig
	if !in.IsRoot {
		var err error
		file, err = LoadFile(FilePath(getenv))
		res.FileErr = err
	}
	res.WrapperPath = file.WrapperPath
	res.FileEnabled = file.Enabled
	res.Notices = DeprecationNotices(in.Flags, getenv, file.Endpoint)

	sp := ResolveSpool(in)
	res.Enabled, res.SpoolDir, res.Source = sp.Enabled, sp.Dir, sp.Source
	res.Forced, res.SpoolErr = sp.Forced, sp.Err
	return res
}

// Prepare is the entry-point helper: it scans args for the telemetry flags,
// resolves the configuration and prints the deprecation notices that apply
// (one line each, prefix NoticePrefix, always to stderr). It never probes or
// connects to anything: telemetry writes to a local file spool. An unusable
// spool directory turns telemetry off for the run (noted under -v only).
func Prepare(args []string, getenv func(string) string, isRoot bool, stderr io.Writer) Resolution {
	res := Resolve(Inputs{Flags: ScanArgs(args), Getenv: getenv, IsRoot: isRoot})
	for _, n := range res.Notices {
		_, _ = fmt.Fprintln(stderr, n)
	}
	if res.SpoolErr != nil && res.Verbose {
		_, _ = fmt.Fprintf(stderr, "telemetry disabled: %v\n", res.SpoolErr)
	}
	return res
}
