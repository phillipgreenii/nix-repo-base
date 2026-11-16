package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want Options
		err  bool
	}{
		{"plain command", []string{"nix", "build", "."}, Options{Cmd: []string{"nix", "build", "."}}, false},
		{
			"all flags then --",
			[]string{"--traceparent", "tp", "--otel-spool-dir=/s", "--log-dir", "/d", "--", "nix", "x"},
			Options{Traceparent: "tp", OtelSpoolDir: "/s", RootSpool: true, LogDir: "/d", Cmd: []string{"nix", "x"}},
			false,
		},
		{
			"deprecated endpoint flag is remembered, never an endpoint",
			[]string{"--otlp-endpoint", "http://h:1", "nix"},
			Options{DeprecatedOTLPFlag: true, Cmd: []string{"nix"}},
			false,
		},
		{"repeated -- belongs to CMD", []string{"--", "nix", "--", "a", "--"}, Options{Cmd: []string{"nix", "--", "a", "--"}}, false},
		{"wrapper flag after CMD is CMD's", []string{"nix", "--log-dir", "/x"}, Options{Cmd: []string{"nix", "--log-dir", "/x"}}, false},
		{"flag-looking CMD arg after first --", []string{"--", "--check"}, Options{Cmd: []string{"--check"}}, false},
		{"check without cmd", []string{"--check"}, Options{Check: true, Cmd: []string{}}, false},
		{"min span", []string{"--min-substitute-span=1s", "nix"}, Options{MinSubstituteSpan: time.Second, Cmd: []string{"nix"}}, false},
		{"unknown flag", []string{"--bogus", "nix"}, Options{}, true},
		{"missing value", []string{"--traceparent"}, Options{}, true},
		{"bad duration", []string{"--min-substitute-span", "soon", "nix"}, Options{}, true},
		{"no args", nil, Options{Cmd: []string(nil)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArgs(c.args)
			if c.err {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("err = %v, want ErrUsage", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.MinSubstituteSpan == 0 {
				got.MinSubstituteSpan = DefaultMinSubstituteSpan
			}
			if c.want.MinSubstituteSpan == 0 {
				c.want.MinSubstituteSpan = DefaultMinSubstituteSpan
			}
			if len(got.Cmd) == 0 {
				got.Cmd = c.want.Cmd
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
}

const tomlPath = "/home/u/.config/pn/telemetry.toml"

func TestResolvePrecedenceNonRoot(t *testing.T) {
	toml := files(map[string]string{tomlPath: "enabled = true\nspool_dir = \"/file/spool\"\n"})
	env := envOf(map[string]string{"TRACEPARENT": "env-tp", "XDG_STATE_HOME": "/xdg", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://env:4318"})
	base := Inputs{Getenv: env, Euid: 501, Home: "/home/u", ReadFile: toml}

	in := base
	in.Opts = Options{OtelSpoolDir: "/flag/spool", Traceparent: "flag-tp"}
	if r := Resolve(in); r.SpoolDir != "/flag/spool" || r.SpoolSource != SourceFlag || r.Traceparent != "flag-tp" || r.Root {
		t.Errorf("flag must win: %+v", r)
	}
	if r := Resolve(base); r.SpoolDir != "/file/spool" || r.SpoolSource != SourceFile || r.Traceparent != "env-tp" {
		t.Errorf("file spool_dir must beat the default: %+v", r)
	}
	in = base
	in.ReadFile = files(map[string]string{tomlPath: "enabled = true\n"})
	if r := Resolve(in); r.SpoolDir != "/xdg/otel-spool" || r.SpoolSource != SourceDefault {
		t.Errorf("XDG default: %+v", r)
	}
	in.Getenv = envOf(nil)
	if r := Resolve(in); r.SpoolDir != "/home/u/.local/state/otel-spool" || r.SpoolSource != SourceDefault {
		t.Errorf("HOME default: %+v", r)
	}
	in.Getenv = envOf(map[string]string{"XDG_STATE_HOME": "relative"})
	if r := Resolve(in); r.SpoolDir != "/home/u/.local/state/otel-spool" {
		t.Errorf("a relative XDG_STATE_HOME must be ignored: %+v", r)
	}
	in.Home = ""
	if r := Resolve(in); r.SpoolDir != "" || r.Off == "" {
		t.Errorf("no home and no XDG: %+v", r)
	}
	in = base
	in.ReadFile = files(map[string]string{tomlPath: "enabled = true\nspool_dir = \"rel/dir\"\n"})
	if r := Resolve(in); r.SpoolDir != "/xdg/otel-spool" {
		t.Errorf("a relative spool_dir must be ignored: %+v", r)
	}
}

func TestResolveRelativeFlagBecomesAbsolute(t *testing.T) {
	in := Inputs{Getenv: envOf(nil), Euid: 501, Home: "/home/u", Opts: Options{OtelSpoolDir: "rel"}}
	if r := Resolve(in); !filepath.IsAbs(r.SpoolDir) || filepath.Base(r.SpoolDir) != "rel" {
		t.Errorf("got %+v", r)
	}
}

func TestResolveRootIgnoresSpoolFlagValueEnvAndFile(t *testing.T) {
	// A root wrapper must not read env (sudo drops it) nor any user-owned file,
	// and the spool flag only switches telemetry on; its value is ignored.
	read := false
	in := Inputs{
		Getenv: envOf(map[string]string{"TRACEPARENT": "env-tp", "XDG_STATE_HOME": "/xdg"}),
		Euid:   0, Home: "/Users/u",
		ReadFile: func(string) ([]byte, error) { read = true; return []byte("enabled = true"), nil },
	}
	if r := Resolve(in); r.SpoolDir != "" || r.Traceparent != "" || read || r.Off != OffRootNotWanted || !r.Root {
		t.Errorf("root resolved from env/file: %+v read=%v", r, read)
	}
	in.Opts = Options{OtelSpoolDir: "/user/spool", RootSpool: true, Traceparent: "tp"}
	r := Resolve(in)
	if r.SpoolDir != DefaultRootSpoolDir || r.SpoolSource != SourceRoot || r.Traceparent != "tp" || read || !r.Root {
		t.Errorf("root must use its own spool, whatever the flag says: %+v read=%v", r, read)
	}
	in.Opts = Options{OTLPEndpoint: "http://legacy"}
	if r := Resolve(in); r.SpoolDir != DefaultRootSpoolDir {
		t.Errorf("the legacy root enable input must still enable: %+v", r)
	}
	in.Opts = Options{RootSpool: true}
	in.RootSpoolDir = "/test/root-spool"
	if r := Resolve(in); r.SpoolDir != "/test/root-spool" {
		t.Errorf("test override: %+v", r)
	}
}

func TestResolveInjectedHomeAndGarbageToml(t *testing.T) {
	in := Inputs{
		Getenv: envOf(nil), Euid: 501, Home: "/injected",
		ReadFile: files(map[string]string{"/injected/.config/pn/telemetry.toml": "enabled = true"}),
	}
	if r := Resolve(in); r.SpoolDir != "/injected/.local/state/otel-spool" {
		t.Errorf("injected home not used: %+v", r)
	}
	for _, garbage := range []string{"\x00\x01 not = = toml [[[", `enabled = 42`, ``, `other = "x"`, `endpoint = "http://x"`} {
		in.ReadFile = files(map[string]string{"/injected/.config/pn/telemetry.toml": garbage})
		if r := Resolve(in); r.SpoolDir != "" || r.Off == "" {
			t.Errorf("garbage %q yielded %+v", garbage, r)
		}
	}
	in.Home = ""
	if r := Resolve(in); r.SpoolDir != "" {
		t.Errorf("no home must mean no file: %+v", r)
	}
}

func TestDisabled(t *testing.T) {
	for _, c := range []struct {
		env map[string]string
		off bool
	}{
		{nil, false},
		{map[string]string{"PG_NIX_LOG_DISABLE": "1"}, true},
		{map[string]string{"PG_NIX_LOG_DISABLE": "0"}, false},
		{map[string]string{"OTEL_SDK_DISABLED": "true"}, true},
		{map[string]string{"OTEL_SDK_DISABLED": "TRUE"}, true},
		{map[string]string{"OTEL_SDK_DISABLED": "false"}, false},
	} {
		if off, why := Disabled(envOf(c.env)); off != c.off || (off && why == "") {
			t.Errorf("Disabled(%v) = %v, %q", c.env, off, why)
		}
	}
}

// TestResolveEnabledKey pins the wrapper side of the enable contract: the
// flag enables, otherwise only telemetry.toml enabled = true does; the old
// endpoint key and OTEL_EXPORTER_OTLP_ENDPOINT never enable.
func TestResolveEnabledKey(t *testing.T) {
	env := envOf(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://env:4318"})
	cases := []struct {
		name       string
		toml       string
		opts       Options
		euid       int
		wantDir    string
		wantSource string
		wantOff    string
	}{
		{"enabled=false", "enabled = false", Options{}, 501, "", "", OffByFile},
		{"flag beats enabled=false", "enabled = false", Options{OtelSpoolDir: "/f"}, 501, "/f", SourceFlag, ""},
		{"enabled=true", "enabled = true", Options{}, 501, "/home/u/.local/state/otel-spool", SourceDefault, ""},
		{"endpoint key alone does not enable", "endpoint = \"http://file:4318\"", Options{}, 501, "", "", OffNotEnabled},
		{"env endpoint alone does not enable", "", Options{}, 501, "", "", OffNotEnabled},
		{"endpoint key does not matter when enabled", "enabled = true\nendpoint = \"http://file:4318\"", Options{}, 501, "/home/u/.local/state/otel-spool", SourceDefault, ""},
		{"wrongly typed enabled is garbage: off", "enabled = \"no\"", Options{}, 501, "", "", OffNotEnabled},
		{"root ignores the file", "enabled = true", Options{}, 0, "", "", OffRootNotWanted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Resolve(Inputs{Opts: c.opts, Getenv: env, Euid: c.euid, Home: "/home/u", ReadFile: files(map[string]string{tomlPath: c.toml})})
			if r.SpoolDir != c.wantDir || r.SpoolSource != c.wantSource || r.Off != c.wantOff {
				t.Errorf("got %+v; want dir=%q source=%q off=%q", r, c.wantDir, c.wantSource, c.wantOff)
			}
		})
	}
}

func TestUsageNamesTheSpoolFlag(t *testing.T) {
	if !strings.Contains(Usage, "--otel-spool-dir") || !strings.Contains(Usage, "--otlp-endpoint is deprecated") {
		t.Errorf("usage: %s", Usage)
	}
}
