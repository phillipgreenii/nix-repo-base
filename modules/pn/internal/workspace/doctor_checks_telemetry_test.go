// internal/workspace/doctor_checks_telemetry_test.go
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

func telemetryWS(t *testing.T) (*Workspace, *exec.FakeRunner) {
	t.Helper()
	f := exec.NewFakeRunner()
	return &Workspace{root: t.TempDir(), runner: f, config: &WorkspaceConfig{}}, f
}

// fakeWrapper creates an executable placeholder file so os.Stat succeeds; the
// FakeRunner supplies the behaviour.
func fakeWrapper(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pg-nix-log-wrapped")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func enabledRes(wrapper string) *telemetrycfg.Resolution {
	return &telemetrycfg.Resolution{
		Enabled: true, Endpoint: "http://127.0.0.1:4318", Source: telemetrycfg.SourceFile, WrapperPath: wrapper,
	}
}

var (
	trueVal  = true
	falseVal = false
)

func TestCheckTelemetry_SilentWhenOff(t *testing.T) {
	ws, f := telemetryWS(t)
	for name, res := range map[string]*telemetrycfg.Resolution{
		"nil":                              nil,
		"disabled":                         {Forced: "--no-telemetry", WrapperPath: "/nix/store/x/w"},
		"no ep":                            {WrapperPath: "/nix/store/x/w"},
		"toml enabled=false":               {Forced: telemetrycfg.ForcedByFile, FileEnabled: &falseVal, WrapperPath: "/nix/store/x/w"},
		"toml enabled=false with endpoint": {Forced: telemetrycfg.ForcedByFile, FileEnabled: &falseVal, Endpoint: "", WrapperPath: "/nix/store/x/w"},
	} {
		if got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: res}); len(got) != 0 {
			t.Errorf("%s: expected no findings, got %+v", name, got)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("telemetry off must run nothing, got calls %+v", f.Calls())
	}
}

func TestCheckTelemetry_CallsWrapperCheck(t *testing.T) {
	ws, f := telemetryWS(t)
	w := fakeWrapper(t)
	f.AddResponse(w, []string{"--check"}, exec.Result{Stdout: []byte("endpoint: http://127.0.0.1:4318\nreachable: yes\nlog dir writable: yes\n")}, nil)

	got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: enabledRes(w)})
	if len(got) != 0 {
		t.Fatalf("healthy wrapper: expected no findings, got %+v", got)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != w || len(calls[0].Args) != 1 || calls[0].Args[0] != "--check" {
		t.Fatalf("expected exactly `%s --check`, got %+v", w, calls)
	}
}

func TestCheckTelemetry_CheckFailureIsWarningNotError(t *testing.T) {
	ws, f := telemetryWS(t)
	w := fakeWrapper(t)
	f.AddResponse(w, []string{"--check"},
		exec.Result{ExitCode: 1, Stdout: []byte("endpoint: http://127.0.0.1:4318\nreachable: NO\n"), Stderr: []byte("dial tcp: refused\n")},
		&exec.CommandError{Name: w, Result: exec.Result{ExitCode: 1}})

	got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: enabledRes(w)})
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %+v", got)
	}
	fd := got[0]
	if fd.CheckID != "telemetry" || fd.Severity != SevWarning || fd.Skipped {
		t.Errorf("unexpected finding shape: %+v", fd)
	}
	if !strings.Contains(fd.Message, "reachable: NO") || !strings.Contains(fd.Message, "exit 1") {
		t.Errorf("message should carry the wrapper's diagnostics: %q", fd.Message)
	}
	rep := &DoctorReport{Findings: got}
	if rep.ExitCode(false) != 0 {
		t.Error("a telemetry warning must not fail doctor without --strict")
	}
}

func TestCheckTelemetry_RunnerErrorIsWarning(t *testing.T) {
	ws, f := telemetryWS(t)
	w := fakeWrapper(t)
	f.AddResponse(w, []string{"--check"}, exec.Result{ExitCode: -1}, errors.New("boom"))
	got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: enabledRes(w)})
	if len(got) != 1 || !strings.Contains(got[0].Message, "boom") {
		t.Fatalf("expected a warning carrying the error, got %+v", got)
	}
}

func TestCheckTelemetry_MissingWrapper(t *testing.T) {
	ws, f := telemetryWS(t)
	for name, path := range map[string]string{
		"not configured": "",
		"does not exist": filepath.Join(t.TempDir(), "nope"),
	} {
		got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: enabledRes(path)})
		if len(got) != 1 || got[0].Severity != SevWarning || got[0].CheckID != "telemetry" {
			t.Errorf("%s: expected one telemetry warning, got %+v", name, got)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("must not exec a missing wrapper: %+v", f.Calls())
	}
}

func TestRegisterChecks_IncludesTelemetry(t *testing.T) {
	ws, _ := telemetryWS(t)
	for _, c := range ws.registerChecks() {
		if c.id == "telemetry" {
			return
		}
	}
	t.Fatal("telemetry check is not registered")
}

// A system configured on whose collector is down: pn's start-up probe turned
// telemetry off for the run, but doctor still reports it (warning, no wrapper
// run: the probe already answered).
func TestCheckTelemetry_ConfiguredOnButUnreachableWarns(t *testing.T) {
	ws, f := telemetryWS(t)
	res := &telemetrycfg.Resolution{
		Forced: telemetrycfg.ForcedUnreachable, FileEnabled: &trueVal,
		ProbeErr: errors.New("dial tcp 127.0.0.1:4318: connection refused"), ProbeEndpoint: "http://127.0.0.1:4318", ProbeSource: telemetrycfg.SourceFile,
		WrapperPath: fakeWrapper(t),
	}
	got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: res})
	if len(got) != 1 || got[0].Severity != SevWarning || got[0].CheckID != "telemetry" {
		t.Fatalf("expected one telemetry warning, got %+v", got)
	}
	if !strings.Contains(got[0].Message, "http://127.0.0.1:4318") || !strings.Contains(got[0].Message, "unreachable") {
		t.Errorf("message should name the endpoint and the failure: %q", got[0].Message)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("must not run the wrapper after a failed probe: %+v", f.Calls())
	}
}

// enabled = true with no endpoint anywhere is a misconfiguration worth a warning.
func TestCheckTelemetry_EnabledWithoutEndpointWarns(t *testing.T) {
	ws, _ := telemetryWS(t)
	got := ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: &telemetrycfg.Resolution{FileEnabled: &trueVal}})
	if len(got) != 1 || !strings.Contains(got[0].Message, "no endpoint") {
		t.Fatalf("expected a no-endpoint warning, got %+v", got)
	}
	// A force-off control wins: the operator asked for off, so stay silent.
	got = ws.checkTelemetry(context.Background(), &doctorEnv{ws: ws, telemetry: &telemetrycfg.Resolution{FileEnabled: &trueVal, Forced: "--no-telemetry"}})
	if len(got) != 0 {
		t.Fatalf("forced off must be silent, got %+v", got)
	}
}
