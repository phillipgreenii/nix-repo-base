package config

import (
	"errors"
	"os"
	"reflect"
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
			[]string{"--traceparent", "tp", "--otlp-endpoint=http://h:1", "--log-dir", "/d", "--", "nix", "x"},
			Options{Traceparent: "tp", OTLPEndpoint: "http://h:1", LogDir: "/d", Cmd: []string{"nix", "x"}},
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

func TestResolvePrecedenceNonRoot(t *testing.T) {
	toml := files(map[string]string{"/home/u/.config/pn/telemetry.toml": `endpoint = "http://file:4318"`})
	env := envOf(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://env:4318", "TRACEPARENT": "env-tp"})
	base := Inputs{Getenv: env, Euid: 501, Home: "/home/u", ReadFile: toml}

	in := base
	in.Opts = Options{OTLPEndpoint: "http://flag:4318", Traceparent: "flag-tp"}
	if r := Resolve(in); r.Endpoint != "http://flag:4318" || r.EndpointSource != SourceFlag || r.Traceparent != "flag-tp" {
		t.Errorf("flag must win: %+v", r)
	}
	if r := Resolve(base); r.Endpoint != "http://env:4318" || r.EndpointSource != SourceEnv || r.Traceparent != "env-tp" {
		t.Errorf("env must beat file: %+v", r)
	}
	in = base
	in.Getenv = envOf(nil)
	if r := Resolve(in); r.Endpoint != "http://file:4318" || r.EndpointSource != SourceFile {
		t.Errorf("file is the last resort: %+v", r)
	}
}

func TestResolveRootReadsFlagsOnly(t *testing.T) {
	// A root wrapper must not read env (sudo drops it) nor any user-owned file.
	read := false
	in := Inputs{
		Getenv: envOf(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://env:4318", "TRACEPARENT": "env-tp"}),
		Euid:   0, Home: "/Users/u",
		ReadFile: func(string) ([]byte, error) { read = true; return []byte(`endpoint = "http://file"`), nil },
	}
	if r := Resolve(in); r.Endpoint != "" || r.Traceparent != "" || read {
		t.Errorf("root resolved from env/file: %+v read=%v", r, read)
	}
	in.Opts = Options{OTLPEndpoint: "http://flag:4318", Traceparent: "tp"}
	if r := Resolve(in); r.Endpoint != "http://flag:4318" || r.Traceparent != "tp" || read {
		t.Errorf("root must honour flags: %+v read=%v", r, read)
	}
}

func TestResolveInjectedHomeAndGarbageToml(t *testing.T) {
	in := Inputs{
		Getenv: envOf(nil), Euid: 501, Home: "/injected",
		ReadFile: files(map[string]string{"/injected/.config/pn/telemetry.toml": `endpoint = "http://injected:4318"`}),
	}
	if r := Resolve(in); r.Endpoint != "http://injected:4318" {
		t.Errorf("injected home not used: %+v", r)
	}
	for _, garbage := range []string{"\x00\x01 not = = toml [[[", `endpoint = 42`, ``, `other = "x"`} {
		in.ReadFile = files(map[string]string{"/injected/.config/pn/telemetry.toml": garbage})
		if r := Resolve(in); r.Endpoint != "" {
			t.Errorf("garbage %q yielded endpoint %q", garbage, r.Endpoint)
		}
	}
	in.Home = ""
	if r := Resolve(in); r.Endpoint != "" {
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
