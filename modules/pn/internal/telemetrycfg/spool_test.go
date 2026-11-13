package telemetrycfg

import (
	"strings"
	"testing"
)

func TestResolveSpool_Precedence(t *testing.T) {
	on := writeTOML(t, "enabled = true\n")
	off := writeTOML(t, "enabled = false\n")
	onDir := writeTOML(t, "enabled = true\nspool_dir = \"/srv/spool\"\n")
	onRel := writeTOML(t, "enabled = true\nspool_dir = \"rel/spool\"\n")
	absent := writeTOML(t, "wrapper_path = \"/nix/store/x\"\n")
	with := func(m map[string]string, kv ...string) map[string]string {
		c := map[string]string{}
		for k, v := range m {
			c[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			c[kv[i]] = kv[i+1]
		}
		return c
	}
	tests := []struct {
		name       string
		in         Inputs
		wantOn     bool
		wantDir    string
		wantSource string
		wantForced string
		wantErr    bool
	}{
		{"no-telemetry beats flag", Inputs{Flags: Flags{NoTelemetry: true, SpoolDir: "/a"}, Getenv: envMap(on)}, false, "", "", "--no-telemetry", false},
		{"wrap disable", Inputs{Flags: Flags{SpoolDir: "/a"}, Getenv: envMap(with(on, EnvWrapDisable, "1"))}, false, "", "", EnvWrapDisable + "=1", false},
		{"sdk disabled", Inputs{Getenv: envMap(with(on, EnvSDKDisabled, "TRUE"))}, false, "", "", EnvSDKDisabled + "=true", false},
		{"flag beats enabled=false", Inputs{Flags: Flags{SpoolDir: "/flag/spool"}, Getenv: envMap(off)}, true, "/flag/spool", SpoolSourceFlag, "", false},
		{"flag beats file spool_dir", Inputs{Flags: Flags{SpoolDir: "/flag/spool"}, Getenv: envMap(onDir)}, true, "/flag/spool", SpoolSourceFlag, "", false},
		{"flag relative rejected", Inputs{Flags: Flags{SpoolDir: "rel"}, Getenv: envMap(on)}, false, "", "", "", true},
		{"enabled=false is off", Inputs{Getenv: envMap(with(off, "XDG_STATE_HOME", "/st"))}, false, "", "", ForcedByFile, false},
		{"enabled=true XDG default", Inputs{Getenv: envMap(with(on, "XDG_STATE_HOME", "/st"))}, true, "/st/otel-spool", SpoolSourceDefault, "", false},
		{"enabled=true HOME fallback", Inputs{Getenv: envMap(with(on, "HOME", "/home/u"))}, true, "/home/u/.local/state/otel-spool", SpoolSourceDefault, "", false},
		{"enabled=true no XDG no HOME", Inputs{Getenv: envMap(on)}, false, "", "", "", true},
		{"spool_dir overrides default", Inputs{Getenv: envMap(with(onDir, "XDG_STATE_HOME", "/st"))}, true, "/srv/spool", SpoolSourceFile, "", false},
		{"file relative spool_dir rejected", Inputs{Getenv: envMap(onRel)}, false, "", "", "", true},
		{"relative XDG_STATE_HOME rejected", Inputs{Getenv: envMap(with(on, "XDG_STATE_HOME", "st"))}, false, "", "", "", true},
		{"absent enabled is off", Inputs{Getenv: envMap(with(absent, "XDG_STATE_HOME", "/st"))}, false, "", "", "", false},
		{"no file is off", Inputs{Getenv: envMap(map[string]string{"XDG_CONFIG_HOME": t.TempDir()})}, false, "", "", "", false},
		{"root ignores user file", Inputs{IsRoot: true, Getenv: envMap(with(onDir, "XDG_STATE_HOME", "/st"))}, false, "", "", "", false},
		{"root on only with flag", Inputs{IsRoot: true, Flags: Flags{SpoolDir: "/var/spool/x"}, Getenv: envMap(off)}, true, "/var/spool/x", SpoolSourceFlag, "", false},
		{"root still forced off", Inputs{IsRoot: true, Flags: Flags{SpoolDir: "/x", NoTelemetry: true}, Getenv: envMap(on)}, false, "", "", "--no-telemetry", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSpool(tc.in)
			if got.Enabled != tc.wantOn || got.Dir != tc.wantDir || got.Source != tc.wantSource || got.Forced != tc.wantForced || (got.Err != nil) != tc.wantErr {
				t.Fatalf("ResolveSpool = %+v; want on=%v dir=%q source=%q forced=%q err=%v", got, tc.wantOn, tc.wantDir, tc.wantSource, tc.wantForced, tc.wantErr)
			}
		})
	}
}

func TestScanArgs_SpoolDir(t *testing.T) {
	if got := ScanArgs([]string{"--otel-spool-dir", "/a", "x"}).SpoolDir; got != "/a" {
		t.Fatalf("separate form = %q", got)
	}
	if got := ScanArgs([]string{"--otel-spool-dir=/b"}).SpoolDir; got != "/b" {
		t.Fatalf("equals form = %q", got)
	}
	if got := ScanArgs([]string{"--", "--otel-spool-dir=/c"}).SpoolDir; got != "" {
		t.Fatalf("after -- = %q", got)
	}
}

func TestNotices(t *testing.T) {
	const p = "pn: telemetry: "
	if got := NoticeFlagEndpoint(); got != p+"--otlp-endpoint is deprecated and ignored; use --otel-spool-dir" {
		t.Fatalf("flag notice = %q", got)
	}
	if got := NoticeEnvEndpoint(); got != p+"OTEL_EXPORTER_OTLP_ENDPOINT is deprecated and ignored; telemetry now writes to a file spool (set spool_dir or --otel-spool-dir)" {
		t.Fatalf("env notice = %q", got)
	}
	if got := NoticeLeftoverEndpointKey(); got != p+"telemetry.toml key \"endpoint\" is deprecated and ignored; telemetry now writes to a file spool (set spool_dir or --otel-spool-dir)" {
		t.Fatalf("key notice = %q", got)
	}
	if got := DeprecationNotices(Flags{}, envMap(nil), ""); len(got) != 0 {
		t.Fatalf("no triggers produced %v", got)
	}
	got := DeprecationNotices(Flags{Endpoint: "http://x"}, envMap(map[string]string{EnvEndpoint: "http://e"}), "http://f")
	if len(got) != 3 {
		t.Fatalf("all triggers = %v", got)
	}
	for _, l := range got {
		if !strings.HasPrefix(l, p) || strings.Contains(l, "\n") {
			t.Fatalf("bad notice line %q", l)
		}
	}
}
