// Package config parses the wrapper's own flags (W-1) and resolves the
// telemetry inputs (endpoint, trace parent) with the contract's precedence:
// flags win over env, env wins over the per-user config file, and a root
// wrapper reads neither env nor the file for those inputs (sudo drops env,
// and root MUST NOT read user-owned files).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultMinSubstituteSpan is the shortest substitution that still gets its
// own span (ADR 0028 item 1); shorter ones are counted in metrics only.
const DefaultMinSubstituteSpan = 250 * time.Millisecond

// Options is the parsed command line.
type Options struct {
	Traceparent       string
	OTLPEndpoint      string
	LogDir            string
	MinSubstituteSpan time.Duration
	Check             bool
	Help              bool
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
		case "--traceparent", "--otlp-endpoint", "--log-dir", "--min-substitute-span":
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
				o.OTLPEndpoint = val
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
const Usage = `usage: pg-nix-log-wrapped [--traceparent TP] [--otlp-endpoint URL] [--log-dir DIR] [--] CMD ARGS...
       pg-nix-log-wrapped --check [--otlp-endpoint URL] [--log-dir DIR]

Runs CMD unchanged and, when an OTLP/HTTP endpoint resolves, turns nix's
--json-log-path activity stream into OpenTelemetry spans and metrics.

Endpoint precedence (non-root): --otlp-endpoint, then OTEL_EXPORTER_OTLP_ENDPOINT,
then "endpoint" in ~/.config/pn/telemetry.toml. A root wrapper reads flags only.

Escape hatches: PG_NIX_LOG_DISABLE=1 or OTEL_SDK_DISABLED=true run CMD
unmodified; PG_NIX_LOG_DEBUG=1 prints diagnostics to stderr.
`

// Sources name where a resolved value came from, for --check.
const (
	SourceFlag = "flag"
	SourceEnv  = "env"
	SourceFile = "file"
)

// Inputs are the ambient facts Resolve consults. Everything is injected so
// tests need no real home directory or environment.
type Inputs struct {
	Opts     Options
	Getenv   func(string) string
	Euid     int
	Home     string
	ReadFile func(string) ([]byte, error)
}

// Resolved is the telemetry configuration after precedence is applied.
type Resolved struct {
	Endpoint       string
	EndpointSource string
	Traceparent    string
}

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
	Endpoint string `toml:"endpoint"`
}

// Resolve applies the W-1 precedence. A root wrapper (Euid 0) uses flags only.
func Resolve(in Inputs) Resolved {
	var r Resolved
	r.Traceparent = in.Opts.Traceparent
	if in.Opts.OTLPEndpoint != "" {
		r.Endpoint, r.EndpointSource = in.Opts.OTLPEndpoint, SourceFlag
	}
	if in.Euid == 0 {
		return r
	}
	if r.Traceparent == "" {
		r.Traceparent = in.Getenv("TRACEPARENT")
	}
	if r.Endpoint == "" {
		if v := in.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
			r.Endpoint, r.EndpointSource = v, SourceEnv
		}
	}
	if r.Endpoint == "" && in.Home != "" && in.ReadFile != nil {
		if b, err := in.ReadFile(strings.TrimRight(in.Home, "/") + "/" + TOMLRelPath); err == nil {
			var fc fileConfig
			// A garbage file is treated as absent: the wrapper fails open.
			if toml.Unmarshal(b, &fc) == nil && fc.Endpoint != "" {
				r.Endpoint, r.EndpointSource = fc.Endpoint, SourceFile
			}
		}
	}
	return r
}
