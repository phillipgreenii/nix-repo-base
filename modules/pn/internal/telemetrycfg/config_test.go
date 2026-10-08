package telemetrycfg

import (
	"os"
	"path/filepath"
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

func TestResolve_Precedence(t *testing.T) {
	fileEnv := writeTOML(t, "endpoint = \"http://file:4318\"\nwrapper_path = \"/nix/store/x-w/bin/w\"\n")
	withEnvVar := map[string]string{"XDG_CONFIG_HOME": fileEnv["XDG_CONFIG_HOME"], EnvEndpoint: "http://env:4318"}

	tests := []struct {
		name       string
		in         Inputs
		wantOn     bool
		wantEP     string
		wantSource string
	}{
		{"flag beats env and file", Inputs{Flags: Flags{Endpoint: "http://flag:4318"}, Getenv: envMap(withEnvVar)}, true, "http://flag:4318", SourceFlag},
		{"env beats file", Inputs{Getenv: envMap(withEnvVar)}, true, "http://env:4318", SourceEnv},
		{"file alone", Inputs{Getenv: envMap(fileEnv)}, true, "http://file:4318", SourceFile},
		{"nothing resolves", Inputs{Getenv: envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()})}, false, "", SourceNone},
		{"root never reads the user file", Inputs{IsRoot: true, Getenv: envMap(fileEnv)}, false, "", SourceNone},
		{"root still honours env", Inputs{IsRoot: true, Getenv: envMap(withEnvVar)}, true, "http://env:4318", SourceEnv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.in)
			if got.Enabled != tc.wantOn || got.Endpoint != tc.wantEP || got.Source != tc.wantSource {
				t.Fatalf("Resolve = %+v; want enabled=%v endpoint=%q source=%q", got, tc.wantOn, tc.wantEP, tc.wantSource)
			}
		})
	}
}

func TestResolve_ForcedOff(t *testing.T) {
	base := writeTOML(t, "endpoint = \"http://file:4318\"\n")
	base[EnvEndpoint] = "http://env:4318"
	for _, tc := range []struct {
		name   string
		flags  Flags
		extra  map[string]string
		forced string
	}{
		{"--no-telemetry", Flags{NoTelemetry: true, Endpoint: "http://flag:4318"}, nil, "--no-telemetry"},
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
			if got.Enabled || got.Endpoint != "" {
				t.Fatalf("expected telemetry forced off, got %+v", got)
			}
			if got.Forced != tc.forced {
				t.Fatalf("Forced = %q; want %q", got.Forced, tc.forced)
			}
		})
	}
	// Not-quite-values do not force off.
	got := Resolve(Inputs{Getenv: envMap(map[string]string{EnvWrapDisable: "0", EnvSDKDisabled: "false", EnvEndpoint: "http://e:4318", "XDG_CONFIG_HOME": t.TempDir()})})
	if !got.Enabled {
		t.Fatalf("PG_NIX_LOG_DISABLE=0 / OTEL_SDK_DISABLED=false must not force off: %+v", got)
	}
}

func TestResolve_WrapperPathFromFile(t *testing.T) {
	env := writeTOML(t, "endpoint = \"http://file:4318\"\nwrapper_path = \"/nix/store/abc-w/bin/pg-nix-log-wrapped\"\n")
	got := Resolve(Inputs{Getenv: envMap(env)})
	if got.WrapperPath != "/nix/store/abc-w/bin/pg-nix-log-wrapped" {
		t.Fatalf("WrapperPath = %q", got.WrapperPath)
	}
}

func TestResolve_MalformedFileIsNonFatal(t *testing.T) {
	env := writeTOML(t, "endpoint = [this is not toml")
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
		{"endpoint spaced", []string{"--otlp-endpoint", "http://x:4318", "workspace"}, Flags{Endpoint: "http://x:4318"}},
		{"endpoint equals", []string{"--otlp-endpoint=http://x:4318", "workspace"}, Flags{Endpoint: "http://x:4318"}},
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

// TestResolve_EnabledKeyMatrix is the precedence matrix for telemetry.toml
// `enabled` against the flag, the environment and the force-off controls
// (ADR 0028, "Runtime configuration contract").
func TestResolve_EnabledKeyMatrix(t *testing.T) {
	const (
		fileEP = "http://file:4318"
		envEP  = "http://env:4318"
		flagEP = "http://flag:4318"
	)
	tests := []struct {
		name       string
		toml       string // "" = no file at all
		flags      Flags
		env        map[string]string
		isRoot     bool
		wantOn     bool
		wantEP     string
		wantSource string
		wantForced string
	}{
		{name: "absent key, file endpoint: on (legacy rule)", toml: `endpoint = "` + fileEP + `"`, wantOn: true, wantEP: fileEP, wantSource: SourceFile},
		{name: "enabled true, file endpoint: on", toml: "enabled = true\nendpoint = \"" + fileEP + "\"", wantOn: true, wantEP: fileEP, wantSource: SourceFile},
		{name: "enabled true, env endpoint, no file endpoint: on", toml: "enabled = true", env: map[string]string{EnvEndpoint: envEP}, wantOn: true, wantEP: envEP, wantSource: SourceEnv},
		{name: "enabled true, flag endpoint: on", toml: "enabled = true", flags: Flags{Endpoint: flagEP}, wantOn: true, wantEP: flagEP, wantSource: SourceFlag},
		{name: "enabled true, env beats file endpoint", toml: "enabled = true\nendpoint = \"" + fileEP + "\"", env: map[string]string{EnvEndpoint: envEP}, wantOn: true, wantEP: envEP, wantSource: SourceEnv},
		{name: "enabled true, no endpoint anywhere: off, not forced", toml: "enabled = true"},
		{name: "enabled false, file endpoint: off", toml: "enabled = false\nendpoint = \"" + fileEP + "\"", wantForced: ForcedByFile},
		{name: "enabled false beats env endpoint", toml: "enabled = false\nendpoint = \"" + fileEP + "\"", env: map[string]string{EnvEndpoint: envEP}, wantForced: ForcedByFile},
		{name: "enabled false, flag endpoint wins (explicit per-run opt-in)", toml: "enabled = false\nendpoint = \"" + fileEP + "\"", flags: Flags{Endpoint: flagEP}, wantOn: true, wantEP: flagEP, wantSource: SourceFlag},
		{name: "force-off beats enabled true", toml: "enabled = true\nendpoint = \"" + fileEP + "\"", flags: Flags{NoTelemetry: true}, wantForced: "--no-telemetry"},
		{name: "force-off beats enabled false (force-off reason wins)", toml: "enabled = false", env: map[string]string{EnvWrapDisable: "1"}, wantForced: EnvWrapDisable + "=1"},
		{name: "force-off beats flag endpoint and enabled false", toml: "enabled = false", flags: Flags{Endpoint: flagEP}, env: map[string]string{EnvSDKDisabled: "true"}, wantForced: EnvSDKDisabled + "=true"},
		{name: "no file: env endpoint on", env: map[string]string{EnvEndpoint: envEP}, wantOn: true, wantEP: envEP, wantSource: SourceEnv},
		{name: "root never reads the file, so enabled false is not seen", toml: "enabled = false", env: map[string]string{EnvEndpoint: envEP}, isRoot: true, wantOn: true, wantEP: envEP, wantSource: SourceEnv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"XDG_CONFIG_HOME": t.TempDir()}
			if tc.toml != "" {
				env = writeTOML(t, tc.toml)
			}
			for k, v := range tc.env {
				env[k] = v
			}
			got := Resolve(Inputs{Flags: tc.flags, Getenv: envMap(env), IsRoot: tc.isRoot})
			if got.Enabled != tc.wantOn || got.Endpoint != tc.wantEP || got.Source != tc.wantSource || got.Forced != tc.wantForced {
				t.Fatalf("Resolve = %+v; want on=%v endpoint=%q source=%q forced=%q", got, tc.wantOn, tc.wantEP, tc.wantSource, tc.wantForced)
			}
		})
	}
}

func TestResolve_FileEnabledIsReported(t *testing.T) {
	for _, tc := range []struct {
		body string
		want *bool
	}{
		{"endpoint = \"http://e\"", nil},
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
