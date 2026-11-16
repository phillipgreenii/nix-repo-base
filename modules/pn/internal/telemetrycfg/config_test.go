package telemetrycfg

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// writeTOML writes a telemetry.toml under a temp XDG_CONFIG_HOME and returns
// the env that points at it.
func writeTOML(t *testing.T, body string) map[string]string {
	t.Helper()
	xdg := t.TempDir()
	dir := filepath.Join(xdg, "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "telemetry.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"XDG_CONFIG_HOME": xdg}
}

func TestResolve_MapsSpoolResolution(t *testing.T) {
	env := writeTOML(t, "enabled = true\nspool_dir = \"/srv/spool\"\nwrapper_path = \"/nix/store/x-w/bin/w\"\n")
	got := Resolve(Inputs{Getenv: envMap(env)})
	if !got.Enabled || got.SpoolDir != "/srv/spool" || got.Source != SourceFile || got.WrapperPath != "/nix/store/x-w/bin/w" {
		t.Fatalf("Resolve = %+v", got)
	}
	if got := Resolve(Inputs{Flags: Flags{SpoolDir: "/flag/s"}, Getenv: envMap(env)}); got.SpoolDir != "/flag/s" || got.Source != SourceFlag {
		t.Fatalf("flag must beat spool_dir: %+v", got)
	}
	if got := Resolve(Inputs{Flags: Flags{SpoolDir: "rel"}, Getenv: envMap(env)}); got.Enabled || got.SpoolErr == nil {
		t.Fatalf("relative flag dir must leave telemetry off with SpoolErr: %+v", got)
	}
	if got := Resolve(Inputs{Getenv: envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()})}); got.Enabled || got.Source != SourceNone {
		t.Fatalf("no file must be off: %+v", got)
	}
}

// TestResolve_EndpointNoLongerEnables pins the cutover: the retired endpoint
// inputs never turn telemetry on, and each yields exactly its notice.
func TestResolve_EndpointNoLongerEnables(t *testing.T) {
	env := writeTOML(t, "endpoint = \"http://file:4318\"\n")
	env[EnvEndpoint] = "http://env:4318"
	got := Resolve(Inputs{Flags: Flags{Endpoint: "http://flag:4318"}, Getenv: envMap(env)})
	if got.Enabled || got.SpoolDir != "" {
		t.Fatalf("endpoint inputs must not enable telemetry: %+v", got)
	}
	want := []string{NoticeFlagEndpoint(), NoticeEnvEndpoint(), NoticeLeftoverEndpointKey()}
	if !reflect.DeepEqual(got.Notices, want) {
		t.Fatalf("Notices = %q; want %q", got.Notices, want)
	}
	// enabled = true with a leftover endpoint key: on, plus the one notice.
	env2 := writeTOML(t, "enabled = true\nendpoint = \"http://file:4318\"\n")
	env2["XDG_STATE_HOME"] = "/st"
	got = Resolve(Inputs{Getenv: envMap(env2)})
	if !got.Enabled || got.SpoolDir != "/st/otel-spool" || !reflect.DeepEqual(got.Notices, []string{NoticeLeftoverEndpointKey()}) {
		t.Fatalf("Resolve = %+v", got)
	}
}

func TestPrepare_PrintsNoticesAndNothingElse(t *testing.T) {
	env := writeTOML(t, "endpoint = \"http://file:4318\"\n")
	var stderr bytes.Buffer
	res := Prepare([]string{"workspace"}, envMap(env), false, &stderr)
	if res.Enabled || stderr.String() != NoticeLeftoverEndpointKey()+"\n" {
		t.Fatalf("res=%+v stderr=%q", res, stderr.String())
	}
	stderr.Reset()
	Prepare([]string{"--otlp-endpoint", "http://x:4318"}, envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()}), false, &stderr)
	if stderr.String() != NoticeFlagEndpoint()+"\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// TestPrepare_NoopPathWritesNothing is the Null Object contract: unconfigured
// or forced-off, resolving telemetry prints nothing (even under -v), leaks no
// goroutine and creates no file under HOME.
func TestPrepare_NoopPathWritesNothing(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}
	before := runtime.NumGoroutine()
	for _, args := range [][]string{{"-v", "workspace", "status"}, {"-v", "--no-telemetry", "workspace", "status"}} {
		var stderr bytes.Buffer
		if res := Prepare(args, envMap(env), false, &stderr); res.Enabled || stderr.Len() != 0 {
			t.Fatalf("args %v: res=%+v stderr=%q", args, res, stderr.String())
		}
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files created under HOME: %v %v", entries, err)
	}
}

func TestPrepare_UnusableSpoolDirNotedUnderVerboseOnly(t *testing.T) {
	var quiet, loud bytes.Buffer
	Prepare([]string{"--otel-spool-dir", "rel"}, envMap(nil), false, &quiet)
	Prepare([]string{"-v", "--otel-spool-dir", "rel"}, envMap(nil), false, &loud)
	if quiet.Len() != 0 || !strings.HasPrefix(loud.String(), "telemetry disabled: ") {
		t.Fatalf("quiet=%q loud=%q", quiet.String(), loud.String())
	}
}

func TestResolve_ForcedOff(t *testing.T) {
	base := writeTOML(t, "enabled = true\n")
	for _, tc := range []struct {
		name   string
		flags  Flags
		extra  map[string]string
		forced string
	}{
		{"--no-telemetry", Flags{NoTelemetry: true, SpoolDir: "/flag/s"}, nil, "--no-telemetry"},
		{"PG_NIX_LOG_DISABLE", Flags{}, map[string]string{EnvWrapDisable: "1"}, "PG_NIX_LOG_DISABLE=1"},
		{"OTEL_SDK_DISABLED", Flags{}, map[string]string{EnvSDKDisabled: "true"}, "OTEL_SDK_DISABLED=true"},
		{"OTEL_SDK_DISABLED case-insensitive", Flags{}, map[string]string{EnvSDKDisabled: "TRUE"}, "OTEL_SDK_DISABLED=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for k, v := range tc.extra {
				env[k] = v
			}
			got := Resolve(Inputs{Flags: tc.flags, Getenv: envMap(env)})
			if got.Enabled || got.SpoolDir != "" {
				t.Fatalf("expected telemetry forced off, got %+v", got)
			}
			if got.Forced != tc.forced {
				t.Fatalf("Forced = %q; want %q", got.Forced, tc.forced)
			}
		})
	}
	// Not-quite-values do not force off.
	got := Resolve(Inputs{Getenv: envMap(map[string]string{EnvWrapDisable: "0", EnvSDKDisabled: "false", "XDG_CONFIG_HOME": writeTOML(t, "enabled = true")["XDG_CONFIG_HOME"], "XDG_STATE_HOME": "/st"})})
	if !got.Enabled {
		t.Fatalf("PG_NIX_LOG_DISABLE=0 / OTEL_SDK_DISABLED=false must not force off: %+v", got)
	}
}

func TestResolve_WrapperPathFromFile(t *testing.T) {
	env := writeTOML(t, "enabled = true\nwrapper_path = \"/nix/store/abc-w/bin/pg-nix-log-wrapped\"\n")
	got := Resolve(Inputs{Getenv: envMap(env)})
	if got.WrapperPath != "/nix/store/abc-w/bin/pg-nix-log-wrapped" {
		t.Fatalf("WrapperPath = %q", got.WrapperPath)
	}
}

func TestResolve_MalformedFileIsNonFatal(t *testing.T) {
	env := writeTOML(t, "enabled = [this is not toml")
	got := Resolve(Inputs{Getenv: envMap(env)})
	if got.Enabled {
		t.Fatalf("malformed file must not enable telemetry: %+v", got)
	}
	if got.FileErr == nil {
		t.Fatal("expected FileErr to carry the parse error for -v diagnostics")
	}
}

func TestFilePath(t *testing.T) {
	if got := FilePath(envMap(map[string]string{"XDG_CONFIG_HOME": "/x"})); got != "/x/pn/telemetry.toml" {
		t.Errorf("XDG: %q", got)
	}
	if got := FilePath(envMap(map[string]string{"HOME": "/h"})); got != "/h/.config/pn/telemetry.toml" {
		t.Errorf("HOME: %q", got)
	}
	if got := FilePath(envMap(nil)); got != "" {
		t.Errorf("neither: %q", got)
	}
}

func TestLoadFile_MissingIsZero(t *testing.T) {
	c, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || c != (FileConfig{}) {
		t.Fatalf("missing file: %+v, %v", c, err)
	}
}

func TestScanArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Flags
	}{
		{"none", []string{"workspace", "status"}, Flags{}},
		{"no-telemetry anywhere", []string{"workspace", "status", "--no-telemetry"}, Flags{NoTelemetry: true}},
		{"leading global flags", []string{"--no-telemetry", "-v", "workspace", "status"}, Flags{NoTelemetry: true, Verbose: true}},
		{"long verbose", []string{"workspace", "--verbose", "status"}, Flags{Verbose: true}},
		{"endpoint spaced (deprecated, still scanned)", []string{"--otlp-endpoint", "http://x:4318", "workspace"}, Flags{Endpoint: "http://x:4318"}},
		{"endpoint equals (deprecated, still scanned)", []string{"--otlp-endpoint=http://x:4318", "workspace"}, Flags{Endpoint: "http://x:4318"}},
		{"stops at --", []string{"workspace", "status", "--", "-v"}, Flags{}},
		{"workspace nix forwards -v to nix", []string{"workspace", "nix", "build", "-v"}, Flags{}},
		{"leading flag before workspace nix still counts", []string{"--no-telemetry", "workspace", "nix", "build", "-v"}, Flags{NoTelemetry: true}},
		{"dangling endpoint flag", []string{"--otlp-endpoint"}, Flags{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanArgs(tc.args); got != tc.want {
				t.Fatalf("ScanArgs(%v) = %+v; want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestNixLogDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"xdg state", map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/pn/nix-logs"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pn/nix-logs"},
		{"neither", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NixLogDir(env(tc.env)); got != tc.want {
				t.Errorf("NixLogDir = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolve_FileEnabledIsReported(t *testing.T) {
	for _, tc := range []struct {
		body string
		want *bool
	}{
		{"wrapper_path = \"/w\"", nil},
		{"enabled = true", boolPtr(true)},
		{"enabled = false", boolPtr(false)},
	} {
		got := Resolve(Inputs{Getenv: envMap(writeTOML(t, tc.body))}).FileEnabled
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%q: FileEnabled = %v; want %v", tc.body, got, tc.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
