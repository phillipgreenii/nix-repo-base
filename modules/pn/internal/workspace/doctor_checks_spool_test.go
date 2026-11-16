// internal/workspace/doctor_checks_spool_test.go
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

var spoolNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type launchctlState int

const (
	lcLoaded launchctlState = iota
	lcNotLoaded
	lcUnavailable
)

// spoolFixture is a temp XDG state home whose otel-spool is the user spool.
type spoolFixture struct {
	state, dir string
}

func newSpoolFixture(t *testing.T) spoolFixture {
	t.Helper()
	state := t.TempDir()
	dir := filepath.Join(state, "otel-spool")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return spoolFixture{state: state, dir: dir}
}

func (fx spoolFixture) addFile(t *testing.T, name string, age time.Duration) {
	t.Helper()
	p := filepath.Join(fx.dir, name)
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := spoolNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// run checks the spool through checkTelemetry with a fake clock, a fake
// launchctl and an injected environment.
func (fx spoolFixture) run(t *testing.T, lc launchctlState, dir string, extraEnv map[string]string) []Finding {
	t.Helper()
	ws, f := telemetryWS(t)
	args := []string{"list", collectorLabel}
	switch lc {
	case lcLoaded:
		f.AddResponse("launchctl", args, exec.Result{}, nil)
	case lcNotLoaded:
		f.AddResponse("launchctl", args, exec.Result{ExitCode: 113}, &exec.CommandError{Name: "launchctl", Result: exec.Result{ExitCode: 113}})
	case lcUnavailable:
		f.AddResponse("launchctl", args, exec.Result{ExitCode: -1}, errors.New("not found"))
	}
	envMap := map[string]string{"XDG_STATE_HOME": fx.state, "XDG_CONFIG_HOME": t.TempDir(), "HOME": t.TempDir()}
	for k, v := range extraEnv {
		envMap[k] = v
	}
	if dir == "" {
		dir = fx.dir
	}
	env := &doctorEnv{
		ws:        ws,
		telemetry: &telemetrycfg.Resolution{FileEnabled: &trueVal},
		spool:     &telemetrycfg.SpoolResolution{Enabled: true, Dir: dir, Source: telemetrycfg.SpoolSourceFile},
		now:       func() time.Time { return spoolNow },
		getenv:    func(k string) string { return envMap[k] },
	}
	return ws.checkTelemetry(context.Background(), env)
}

func TestSpool_OKWhenEmptyOrNeverWritten(t *testing.T) {
	fx := newSpoolFixture(t)
	if got := fx.run(t, lcLoaded, "", nil); len(got) != 0 {
		t.Fatalf("empty spool: %+v", got)
	}
	// never written: the directory does not exist yet
	if got := fx.run(t, lcLoaded, filepath.Join(fx.state, "nope", "otel-spool"), map[string]string{"XDG_STATE_HOME": filepath.Join(fx.state, "nope")}); len(got) != 0 {
		t.Fatalf("never written: %+v", got)
	}
}

func TestSpool_StuckBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   time.Duration
		stuck bool
	}{
		{"29 minutes", 29 * time.Minute, false},
		{"31 minutes", 31 * time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newSpoolFixture(t)
			fx.addFile(t, "new.json", time.Minute)
			fx.addFile(t, "old.json", tc.age)
			got := fx.run(t, lcLoaded, "", nil)
			if !tc.stuck {
				if len(got) != 0 {
					t.Fatalf("unexpected %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Severity != SevWarning || got[0].CheckID != "telemetry" {
				t.Fatalf("got %+v", got)
			}
			m := got[0].Message
			for _, want := range []string{"stuck", fx.dir, "old.json", collectorLabel, "delete_after_read", "fingerprint collision", "writer bug"} {
				if !strings.Contains(m, want) {
					t.Errorf("message lacks %q: %s", want, m)
				}
			}
			if strings.Contains(m, "not loaded") {
				t.Errorf("loaded collector reported not loaded: %s", m)
			}
			if got[0].Manual != "launchctl list "+collectorLabel {
				t.Errorf("manual = %q", got[0].Manual)
			}
			if (&DoctorReport{Findings: got}).ExitCode(false) != 0 || (&DoctorReport{Findings: got}).HasErrors() {
				t.Error("a stuck spool must not fail doctor")
			}
		})
	}
}

func TestSpool_InFlightTmpFilesAreNotStuck(t *testing.T) {
	fx := newSpoolFixture(t)
	fx.addFile(t, "w.json.tmp", 2*time.Hour)
	if got := fx.run(t, lcLoaded, "", nil); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSpool_StuckAndLabelNotLoaded(t *testing.T) {
	fx := newSpoolFixture(t)
	fx.addFile(t, "old.json", time.Hour)
	got := fx.run(t, lcNotLoaded, "", nil)
	if len(got) != 1 || !strings.Contains(got[0].Message, "stuck") || !strings.Contains(got[0].Message, "launchctl reports the label is not loaded") {
		t.Fatalf("%+v", got)
	}
}

func TestSpool_CollectorNotLoadedWarns(t *testing.T) {
	fx := newSpoolFixture(t)
	got := fx.run(t, lcNotLoaded, "", nil)
	if len(got) != 1 || got[0].Severity != SevWarning ||
		!strings.Contains(got[0].Message, collectorLabel+" is not loaded") ||
		!strings.Contains(got[0].Message, "files accumulate in "+fx.dir+" up to the sweep bounds with no collector") {
		t.Fatalf("%+v", got)
	}
	// launchctl unusable: say nothing rather than guess.
	if got := fx.run(t, lcUnavailable, "", nil); len(got) != 0 {
		t.Fatalf("unknown launchctl state: %+v", got)
	}
}

func TestSpool_UnwritableWarnsWithPathAndError(t *testing.T) {
	fx := newSpoolFixture(t)
	blocker := filepath.Join(fx.state, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(blocker, "otel-spool")
	got := fx.run(t, lcLoaded, dir, nil)
	if len(got) != 1 || got[0].Severity != SevWarning || !strings.Contains(got[0].Message, "spool not writable: "+dir) || !strings.Contains(got[0].Message, "not a directory") {
		t.Fatalf("%+v", got)
	}
}

func TestSpool_OverrideDifferingFromCollectorPathWarnsNamingBoth(t *testing.T) {
	fx := newSpoolFixture(t)
	other := filepath.Join(t.TempDir(), "elsewhere")
	got := fx.run(t, lcLoaded, other, nil)
	if len(got) != 1 || !strings.Contains(got[0].Message, other) || !strings.Contains(got[0].Message, fx.dir) || !strings.Contains(got[0].Message, "unread") {
		t.Fatalf("%+v", got)
	}
	// HOME fallback when XDG_STATE_HOME is unset.
	home := t.TempDir()
	hd := filepath.Join(home, ".local", "state", "otel-spool")
	if err := os.MkdirAll(hd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fx.run(t, lcLoaded, hd, map[string]string{"XDG_STATE_HOME": "", "HOME": home}); len(got) != 0 {
		t.Fatalf("HOME default must match: %+v", got)
	}
}

func TestSpool_LeftoverEndpointKeyWarns(t *testing.T) {
	fx := newSpoolFixture(t)
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "pn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "pn", "telemetry.toml"), []byte("enabled = true\nendpoint = \"http://127.0.0.1:4318\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := fx.run(t, lcLoaded, "", map[string]string{"XDG_CONFIG_HOME": cfg})
	if len(got) != 1 || got[0].Severity != SevWarning || !strings.Contains(got[0].Message, "endpoint key") {
		t.Fatalf("%+v", got)
	}
}

func TestSpool_SilentWhenTelemetryOff(t *testing.T) {
	fx := newSpoolFixture(t)
	fx.addFile(t, "old.json", 5*time.Hour)
	ws, f := telemetryWS(t)
	for name, sp := range map[string]*telemetrycfg.SpoolResolution{
		"nil":    nil,
		"forced": {Forced: "--no-telemetry", Dir: fx.dir},
		"off":    {Dir: fx.dir},
	} {
		env := &doctorEnv{ws: ws, spool: sp, telemetry: &telemetrycfg.Resolution{}, now: func() time.Time { return spoolNow }}
		if got := ws.checkTelemetry(context.Background(), env); len(got) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("telemetry off must run nothing: %+v", f.Calls())
	}
}

// The shim comparison matrix: root telemetry is wanted by the new key, the
// deprecated key or both; neither warns.
func TestCheckShim_RootKeyMatrix(t *testing.T) {
	out := telOK + shimsOK + rootGood + "status: telemetry=ok shim=ok\n"
	for _, tc := range []struct {
		body string
		warn bool
	}{
		{`{"rootSpool":true}`, false},
		{`{"rootEndpoint":"http://127.0.0.1:4318"}`, false},
		{`{"rootSpool":true,"rootEndpoint":"http://127.0.0.1:4318"}`, false},
		{`{}`, true},
	} {
		_, shim, _ := shimCheckRun(t, out, 0, rootCfg(t, tc.body))
		if (len(shim) == 1) != tc.warn || len(shim) > 1 {
			t.Errorf("%s: warn=%v got %+v", tc.body, tc.warn, shim)
		}
		if tc.warn && shim[0].Severity != SevWarning {
			t.Errorf("not a warning: %+v", shim[0])
		}
	}
}
