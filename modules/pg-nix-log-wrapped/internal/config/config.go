// Package config parses the wrapper's own flags (W-1) and resolves the
// telemetry inputs (spool directory, trace parent) with the contract's
// precedence: the flag wins over the per-user config file, which wins over the
// XDG default, and a root wrapper reads neither env nor the file (sudo drops
// env, and root MUST NOT read user-owned files).
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultMinSubstituteSpan is the shortest substitution that still gets its
// own span (ADR 0028 item 1); shorter ones are counted in metrics only.
const DefaultMinSubstituteSpan = 250 * time.Millisecond

// Options is the parsed command line.
type Options struct {
	Traceparent string
	// OtelSpoolDir is --otel-spool-dir (a user wrapper's spool directory).
	OtelSpoolDir string
	// RootSpool says root telemetry was asked for by the presence of
	// --otel-spool-dir; its value is ignored for a root wrapper.
	RootSpool bool
	// OTLPEndpoint is the legacy root enable input, still assigned by the shim
	// from the old rootEndpoint key. Non-empty means root telemetry is wanted;
	// the value itself is never used. The --otlp-endpoint flag does NOT set it.
	OTLPEndpoint string
	// DeprecatedOTLPFlag is set when the wrapper itself received the
	// deprecated --otlp-endpoint flag (a no-op).
	DeprecatedOTLPFlag bool
	LogDir             string
	MinSubstituteSpan  time.Duration
	Check              bool
	Help               bool
	// Cmd is CMD followed by its arguments, exactly as given.
	Cmd []string
}

// ErrUsage marks a command-line error (the wrapper's own, not CMD's).
var ErrUsage = errors.New("usage error")

// ParseArgs parses args (without argv[0]). Parsing stops at the first "--"
// (consumed) or at the first argument that is not a flag; everything after is
// CMD and its arguments, untouched (repeated "--" included).
func ParseArgs(args []string) (Options, error) {
	o := Options{MinSubstituteSpan: DefaultMinSubstituteSpan}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		switch name {
		case "--check":
			o.Check = true
		case "--help", "-h":
			o.Help = true
		case "--traceparent", "--otlp-endpoint", "--otel-spool-dir", "--log-dir", "--min-substitute-span":
			if !hasVal {
				if i+1 >= len(args) {
					return o, fmt.Errorf("%w: %s needs a value", ErrUsage, name)
				}
				i++
				val = args[i]
			}
			switch name {
			case "--traceparent":
				o.Traceparent = val
			case "--otlp-endpoint":
				o.DeprecatedOTLPFlag = true
			case "--otel-spool-dir":
				o.OtelSpoolDir = val
				o.RootSpool = true
			case "--log-dir":
				o.LogDir = val
			case "--min-substitute-span":
				d, err := time.ParseDuration(val)
				if err != nil || d < 0 {
					return o, fmt.Errorf("%w: bad --min-substitute-span %q", ErrUsage, val)
				}
				o.MinSubstituteSpan = d
			}
		default:
			return o, fmt.Errorf("%w: unknown flag %s", ErrUsage, name)
		}
		i++
	}
	o.Cmd = args[i:]
	return o, nil
}

// Usage is the --help text.
const Usage = `usage: pg-nix-log-wrapped [--traceparent TP] [--otel-spool-dir DIR] [--log-dir DIR] [--] CMD ARGS...
       pg-nix-log-wrapped --check [--otel-spool-dir DIR] [--log-dir DIR]

Runs CMD unchanged and, when telemetry is on, turns nix's --json-log-path
activity stream into OpenTelemetry spans and metrics written as files into a
spool directory that the collector reads.

Spool directory precedence (non-root): --otel-spool-dir, then "spool_dir" in
~/.config/pn/telemetry.toml, then $XDG_STATE_HOME/otel-spool (else
~/.local/state/otel-spool). Telemetry is on when the flag is given or the file
says "enabled = true". A root wrapper ignores the flag's value and always uses
/var/log/pg-nix-log-wrapped-spool.

--otlp-endpoint is deprecated and does nothing.

Escape hatches: PG_NIX_LOG_DISABLE=1 or OTEL_SDK_DISABLED=true run CMD
unmodified; PG_NIX_LOG_DEBUG=1 prints diagnostics to stderr.
`

// DeprecatedOTLPNotice is printed once when the wrapper receives --otlp-endpoint.
const DeprecatedOTLPNotice = "pg-nix-log-wrapped: --otlp-endpoint is deprecated and ignored; telemetry is written to a spool directory (--otel-spool-dir)\n"

// DefaultRootSpoolDir is where a root wrapper writes its spool: a sibling of
// the private root log directory, readable by the user-run collector.
const DefaultRootSpoolDir = "/var/log/pg-nix-log-wrapped-spool"

// Sources name where a resolved value came from, for --check.
const (
	SourceFlag    = "flag"
	SourceFile    = "file"
	SourceDefault = "default"
	SourceRoot    = "root"
)

// Inputs are the ambient facts Resolve consults. Everything is injected so
// tests need no real home directory or environment.
type Inputs struct {
	Opts     Options
	Getenv   func(string) string
	Euid     int
	Home     string
	ReadFile func(string) ([]byte, error)
	// RootSpoolDir overrides DefaultRootSpoolDir (tests only).
	RootSpoolDir string
}

// Resolved is the telemetry configuration after precedence is applied.
type Resolved struct {
	// SpoolDir is the absolute spool directory, empty when telemetry is off.
	SpoolDir    string
	SpoolSource string
	Traceparent string
	// Root is true for a root wrapper's resolution (the root spool role).
	Root bool
	// Off is non-empty when telemetry is off, naming why. The force-off
	// environment controls are reported by Disabled, not here.
	Off string
}

// Reasons Resolved.Off carries.
const (
	// OffByFile is Off when telemetry.toml says `enabled = false`.
	OffByFile = "telemetry.toml enabled = false"
	// OffNotEnabled is Off when nothing turned telemetry on.
	OffNotEnabled = "telemetry is not enabled (no --otel-spool-dir, no telemetry.toml enabled = true)"
	// OffRootNotWanted is Off for a root wrapper nothing asked to export.
	OffRootNotWanted = "root telemetry not requested"
)

// Disabled reports whether the environment forces telemetry off.
func Disabled(getenv func(string) string) (bool, string) {
	if v := getenv("PG_NIX_LOG_DISABLE"); v == "1" || strings.EqualFold(v, "true") {
		return true, "PG_NIX_LOG_DISABLE is set"
	}
	if strings.EqualFold(strings.TrimSpace(getenv("OTEL_SDK_DISABLED")), "true") {
		return true, "OTEL_SDK_DISABLED=true"
	}
	return false, ""
}

// TOMLRelPath is the per-user config file, relative to the home directory.
const TOMLRelPath = ".config/pn/telemetry.toml"

type fileConfig struct {
	Enabled  *bool  `toml:"enabled"`
	SpoolDir string `toml:"spool_dir"`
}

// Resolve applies the precedence (ADR 0028, "Runtime configuration
// contract"). A root wrapper (Euid 0) turns telemetry on when RootSpool is set
// or the legacy OTLPEndpoint is non-empty, always uses the root spool
// directory, and reads neither env nor files. Otherwise: the --otel-spool-dir
// flag (explicit, beats the file's enabled = false), then the file's enabled
// (absent means off), then spool_dir from the file, then the XDG default.
// OTEL_EXPORTER_OTLP_ENDPOINT and the file's endpoint key play no part.
func Resolve(in Inputs) Resolved {
	var r Resolved
	r.Traceparent = in.Opts.Traceparent
	if in.Euid == 0 {
		r.Root = true
		if !in.Opts.RootSpool && in.Opts.OTLPEndpoint == "" {
			r.Off = OffRootNotWanted
			return r
		}
		r.SpoolDir, r.SpoolSource = in.RootSpoolDir, SourceRoot
		if r.SpoolDir == "" {
			r.SpoolDir = DefaultRootSpoolDir
		}
		return r
	}
	if r.Traceparent == "" {
		r.Traceparent = in.Getenv("TRACEPARENT")
	}
	if v := in.Opts.OtelSpoolDir; v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			r.SpoolDir, r.SpoolSource = abs, SourceFlag
			return r
		}
	}
	var fc fileConfig
	if in.Home != "" && in.ReadFile != nil {
		if b, err := in.ReadFile(strings.TrimRight(in.Home, "/") + "/" + TOMLRelPath); err == nil {
			// A garbage file is treated as absent: the wrapper fails open.
			if toml.Unmarshal(b, &fc) != nil {
				fc = fileConfig{}
			}
		}
	}
	if fc.Enabled == nil {
		r.Off = OffNotEnabled
		return r
	}
	if !*fc.Enabled {
		r.Off = OffByFile
		return r
	}
	switch xdg := in.Getenv("XDG_STATE_HOME"); {
	case filepath.IsAbs(fc.SpoolDir):
		r.SpoolDir, r.SpoolSource = filepath.Clean(fc.SpoolDir), SourceFile
	case filepath.IsAbs(xdg):
		r.SpoolDir, r.SpoolSource = filepath.Join(xdg, "otel-spool"), SourceDefault
	case in.Home != "":
		r.SpoolDir, r.SpoolSource = filepath.Join(in.Home, ".local", "state", "otel-spool"), SourceDefault
	default:
		// Unreachable: the file is only read with a Home.
		r.Off = OffNotEnabled
	}
	return r
}
