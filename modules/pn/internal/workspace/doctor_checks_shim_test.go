// internal/workspace/doctor_checks_shim_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

const (
	telOK    = "endpoint: http://127.0.0.1:4318 (from file)\nreachable: yes\nlog-dir: /x (writable: yes)\n"
	shimsOK  = "shim: nix: shim at /s/nix\nshim: darwin-rebuild: shim at /s/darwin-rebuild\nshim: allowlist: nix build\n"
	rootAbs  = "shim: root-config: absent (/etc/pg-nix-shim.json)\nshim: root-run: would pass through (no root configuration)\n"
	rootGood = "shim: root-config: /etc/pg-nix-shim.json -> /nix/store/a-x.json (store regular file)\nshim: root-run: would wrap (rootEndpoint http://127.0.0.1:4318)\n"
)

// shimCheckRun runs the telemetry then the shim check on one doctorEnv with
// the wrapper printing out and exiting code, returning the findings and the
// number of wrapper runs.
func shimCheckRun(t *testing.T, out string, code int, mutate func(*telemetrycfg.Resolution, *doctorEnv)) (tel, shim []Finding, runs int) {
	t.Helper()
	ws, f := telemetryWS(t)
	w := fakeWrapper(t)
	var err error
	if code != 0 {
		err = &exec.CommandError{Name: w, Result: exec.Result{ExitCode: code}}
	}
	f.AddResponse(w, []string{"--check"}, exec.Result{ExitCode: code, Stdout: []byte(out)}, err)
	res := enabledRes(w)
	env := &doctorEnv{ws: ws, telemetry: res}
	if mutate != nil {
		mutate(res, env)
	}
	tel = ws.checkTelemetry(context.Background(), env)
	shim = ws.checkShim(context.Background(), env)
	return tel, shim, len(f.Calls())
}

func TestCheckShim_HealthyIsSilentAndProbesOnce(t *testing.T) {
	tel, shim, runs := shimCheckRun(t, telOK+shimsOK+rootGood+"status: telemetry=ok shim=ok\n", 0, nil)
	if len(tel) != 0 || len(shim) != 0 {
		t.Fatalf("tel=%+v shim=%+v", tel, shim)
	}
	if runs != 1 {
		t.Errorf("wrapper --check ran %d times, want 1", runs)
	}
}

func TestCheckShim_ShimModulesNotEnabledIsSilent(t *testing.T) {
	// Neither name is a shim and there is no root configuration: the output
	// must be identical to a machine without the modules.
	out := telOK + "shim: nix: NOT a shim: /usr/bin/nix is a real binary shadowing it\nshim: darwin-rebuild: NOT a shim: not found on PATH\n" + rootAbs + "status: telemetry=ok shim=fail\n"
	tel, shim, _ := shimCheckRun(t, out, 1, nil)
	if len(tel) != 0 {
		t.Errorf("a shim-only failure must not raise the telemetry warning: %+v", tel)
	}
	if len(shim) != 0 {
		t.Errorf("modules not enabled: want no finding, got %+v", shim)
	}
}

func TestCheckShim_OneNotAShimWarns(t *testing.T) {
	out := telOK + "shim: nix: shim at /s/nix\nshim: darwin-rebuild: NOT a shim: /sw/bin/darwin-rebuild is a real binary shadowing it\n" + rootGood + "status: telemetry=ok shim=fail\n"
	tel, shim, _ := shimCheckRun(t, out, 1, nil)
	if len(tel) != 0 {
		t.Errorf("telemetry finding for a shim-only failure: %+v", tel)
	}
	if len(shim) != 1 || shim[0].Severity != SevWarning || shim[0].CheckID != "shim" ||
		!strings.Contains(shim[0].Message, "darwin-rebuild: NOT a shim") {
		t.Fatalf("shim = %+v", shim)
	}
	if (&DoctorReport{Findings: shim}).ExitCode(false) != 0 {
		t.Error("a shim warning must not fail doctor without --strict")
	}
}

func TestCheckShim_RootConfigNotAStoreSymlink(t *testing.T) {
	out := telOK + shimsOK + "shim: root-config: /etc/pg-nix-shim.json present but not usable: root configuration is not under the store prefix (not a store symlink)\nshim: root-run: would pass through (root configuration is not under the store prefix)\nstatus: telemetry=ok shim=ok\n"
	_, shim, _ := shimCheckRun(t, out, 0, nil)
	if len(shim) != 1 {
		t.Fatalf("shim = %+v", shim)
	}
	m := shim[0].Message
	if !strings.Contains(m, "root config not a store symlink") || !strings.Contains(m, "0600") || !strings.Contains(m, "non-root doctor") {
		t.Errorf("message = %q", m)
	}
}

func TestCheckShim_UserShimWithoutRootConfigWarns(t *testing.T) {
	_, shim, _ := shimCheckRun(t, telOK+shimsOK+rootAbs+"status: telemetry=ok shim=ok\n", 0, nil)
	if len(shim) != 1 || !strings.Contains(shim[0].Message, "root runs") || !strings.Contains(shim[0].Message, "unreadable to a non-root doctor") {
		t.Fatalf("shim = %+v", shim)
	}
}

func rootCfg(t *testing.T, body string) func(*telemetrycfg.Resolution, *doctorEnv) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pg-nix-shim.json")
	if err := os.WriteFile(p, []byte(body), 0o444); err != nil {
		t.Fatal(err)
	}
	return func(_ *telemetrycfg.Resolution, env *doctorEnv) { env.shimRootConfig = p }
}

func TestCheckShim_EndpointMismatch(t *testing.T) {
	out := telOK + shimsOK + rootGood + "status: telemetry=ok shim=ok\n"
	_, shim, _ := shimCheckRun(t, out, 0, rootCfg(t, `{"rootEndpoint":"http://127.0.0.1:9999"}`))
	if len(shim) != 1 || !strings.Contains(shim[0].Message, "http://127.0.0.1:4318") || !strings.Contains(shim[0].Message, "http://127.0.0.1:9999") {
		t.Fatalf("mismatch not reported: %+v", shim)
	}
	if _, shim, _ = shimCheckRun(t, out, 0, rootCfg(t, `{"rootEndpoint":"http://127.0.0.1:4318"}`)); len(shim) != 0 {
		t.Errorf("equal endpoints reported: %+v", shim)
	}
	// Unreadable root file, or the user endpoint not from telemetry.toml: nothing to compare.
	if _, shim, _ = shimCheckRun(t, out, 0, func(_ *telemetrycfg.Resolution, env *doctorEnv) {
		env.shimRootConfig = filepath.Join(t.TempDir(), "nope")
	}); len(shim) != 0 {
		t.Errorf("unreadable file reported: %+v", shim)
	}
	if _, shim, _ = shimCheckRun(t, out, 0, func(res *telemetrycfg.Resolution, env *doctorEnv) {
		rootCfg(t, `{"rootEndpoint":"http://other"}`)(res, env)
		res.Source = telemetrycfg.SourceEnv
	}); len(shim) != 0 {
		t.Errorf("env-sourced endpoint compared: %+v", shim)
	}
}

func TestCheckShim_SilentWhenTelemetryOffOrOldWrapper(t *testing.T) {
	ws, f := telemetryWS(t)
	if got := ws.checkShim(context.Background(), &doctorEnv{ws: ws}); len(got) != 0 {
		t.Errorf("nil telemetry: %+v", got)
	}
	if got := ws.checkShim(context.Background(), &doctorEnv{ws: ws, telemetry: &telemetrycfg.Resolution{WrapperPath: "/nix/store/x/w"}}); len(got) != 0 {
		t.Errorf("telemetry off: %+v", got)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("ran the wrapper: %+v", f.Calls())
	}
	// An older wrapper prints no shim lines.
	if _, shim, _ := shimCheckRun(t, telOK, 0, nil); len(shim) != 0 {
		t.Errorf("old wrapper: %+v", shim)
	}
}

func TestCheckTelemetry_RealTelemetryFailureStillWarnsWithStatusLine(t *testing.T) {
	out := "endpoint: http://127.0.0.1:4318 (from file)\nreachable: no (refused)\nlog-dir: /x (writable: yes)\n" + shimsOK + "status: telemetry=fail shim=ok\n"
	tel, _, _ := shimCheckRun(t, out, 1, nil)
	if len(tel) != 1 || !strings.Contains(tel[0].Message, "reachable: no") || strings.Contains(tel[0].Message, "shim:") {
		t.Fatalf("tel = %+v", tel)
	}
}

func TestRegisterChecks_IncludesShim(t *testing.T) {
	ws, _ := telemetryWS(t)
	for _, c := range ws.registerChecks() {
		if c.id == shimCheckID {
			return
		}
	}
	t.Fatal("shim check is not registered")
}
