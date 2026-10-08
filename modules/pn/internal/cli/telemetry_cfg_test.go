package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// isolateTelemetryEnv points HOME/XDG at a temp dir and clears every telemetry
// control so the developer's real ~/.config/pn/telemetry.toml and shell are
// never read.
func isolateTelemetryEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SDK_DISABLED", "TRACEPARENT",
		"PG_NIX_LOG_DISABLE", "PN_TRACE_HINT",
	} {
		t.Setenv(k, "")
	}
	return home
}

// withTraceCmd registers a hidden `tracecmd` verb that behaves like the root
// span setup (it records a trace id) and prints "payload" to stdout.
func withTraceCmd(t *testing.T) {
	t.Helper()
	prev := addExtraCmdHook
	addExtraCmdHook = func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{
			Use:    "tracecmd",
			Hidden: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				telemetrycfg.RunStateFrom(cmd.Context()).SetTraceID(testTraceID)
				_, _ = cmd.OutOrStdout().Write([]byte("payload\n"))
				return nil
			},
		})
	}
	t.Cleanup(func() { addExtraCmdHook = prev })
}

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	err = executeWithVersion("test", args, &o, &e)
	return o.String(), e.String(), err
}

func TestTraceHint_StderrOnlyUnderVerboseOrEnv(t *testing.T) {
	isolateTelemetryEnv(t)
	withTraceCmd(t)

	baseOut, baseErr, err := run(t, "tracecmd")
	if err != nil || baseErr != "" {
		t.Fatalf("baseline: err=%v stderr=%q", err, baseErr)
	}

	// -v: hint on stderr, stdout byte-identical to the baseline.
	out, errOut, err := run(t, "-v", "tracecmd")
	if err != nil {
		t.Fatal(err)
	}
	if out != baseOut {
		t.Errorf("stdout changed under -v:\n got %q\nwant %q", out, baseOut)
	}
	if errOut != "trace: "+testTraceID+"\n" {
		t.Errorf("stderr under -v = %q", errOut)
	}

	// --verbose, after the verb.
	if _, e, _ := run(t, "tracecmd", "--verbose"); e != "trace: "+testTraceID+"\n" {
		t.Errorf("stderr under --verbose = %q", e)
	}

	// PN_TRACE_HINT=1 without -v.
	t.Setenv("PN_TRACE_HINT", "1")
	out, errOut, _ = run(t, "tracecmd")
	if out != baseOut || errOut != "trace: "+testTraceID+"\n" {
		t.Errorf("PN_TRACE_HINT=1: stdout=%q stderr=%q", out, errOut)
	}
}

func TestTraceHint_OffByDefaultAndWithoutTraceID(t *testing.T) {
	isolateTelemetryEnv(t)
	withTraceCmd(t)
	if _, e, _ := run(t, "tracecmd"); e != "" {
		t.Errorf("no -v, no env: stderr must be empty, got %q", e)
	}
	// -v on a verb that never records a trace id (telemetry off): no hint.
	t.Setenv("PN_TRACE_HINT", "1")
	if _, e, _ := run(t, "--version"); e != "" {
		t.Errorf("no trace id: stderr must be empty, got %q", e)
	}
}

func TestNoTelemetryFlagAccepted(t *testing.T) {
	isolateTelemetryEnv(t)
	for _, args := range [][]string{
		{"--no-telemetry", "--version"},
		{"--version", "--no-telemetry"},
		{"--otlp-endpoint", "http://127.0.0.1:1", "--version"},
	} {
		out, _, err := run(t, args...)
		if err != nil || out != "test\n" {
			t.Errorf("%v: out=%q err=%v", args, out, err)
		}
	}
}

func TestVerbose_UnreachableCollectorLine(t *testing.T) {
	isolateTelemetryEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)

	// Without -v: silent.
	if _, e, _ := run(t, "--version"); e != "" {
		t.Errorf("no -v: stderr must be empty, got %q", e)
	}
	// With -v: exactly the one line.
	_, e, _ := run(t, "-v", "--version")
	want := "telemetry disabled: collector unreachable (http://" + addr + ")\n"
	if e != want {
		t.Errorf("-v stderr = %q; want %q", e, want)
	}
	// --no-telemetry wins: no probe, no line.
	if _, e, _ := run(t, "-v", "--no-telemetry", "--version"); e != "" {
		t.Errorf("--no-telemetry -v: stderr must be empty, got %q", e)
	}
}

// TestDisabledPath_NoFilesUnderHome: with no telemetry configured, running a
// verb creates nothing under HOME and prints nothing extra.
func TestDisabledPath_NoFilesUnderHome(t *testing.T) {
	home := isolateTelemetryEnv(t)
	withTraceCmd(t)
	if _, _, err := run(t, "-v", "tracecmd"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("files created under HOME: %v", entries)
	}
}

func TestRootHelp_HasEscapeHatchTable(t *testing.T) {
	isolateTelemetryEnv(t)
	out, _, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"phillipgreenii.pn.telemetry.enable = false",
		"pn --no-telemetry",
		"PG_NIX_LOG_DISABLE=1",
		"pg-nix-log-wrapped --check",
		"OTEL_SDK_DISABLED=true",
		"PG_NIX_LOG_DEBUG=1",
		"--otlp-endpoint",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pn --help missing %q:\n%s", want, out)
		}
	}
}

func TestWorkspaceHelp_DocumentsTelemetryEnv(t *testing.T) {
	isolateTelemetryEnv(t)
	out, _, err := run(t, "workspace", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SDK_DISABLED", "PG_NIX_LOG_DISABLE",
		"PG_NIX_LOG_DEBUG", "PN_TRACE_HINT", "~/.config/pn/telemetry.toml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pn workspace --help missing %q", want)
		}
	}
}

// TestWorkspaceUpdate_EventLogCarriesTraceID: when the root span recorded a
// trace id, `pn workspace update` stamps it on run_start and run_end in
// events.jsonl (stdout is untouched), and only those two kinds.
func TestWorkspaceUpdate_EventLogCarriesTraceID(t *testing.T) {
	home := isolateTelemetryEnv(t)
	stateHome := filepath.Join(home, "state")

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(`[workspace]
name = "test"
terminal = "myterm"

[repos.myterm]
url = "github:owner/myterm"
`), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	fr := exec.NewFakeRunner()
	repo := filepath.Join(root, "myterm")
	fr.AddResponse("git", []string{"-C", repo, "diff", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "--abbrev-ref", "@{u}"},
		exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	fr.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	fr.AddResponse("git", []string{"-C", repo, "rev-parse", "HEAD"},
		exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)
	w, err := workspace.Open(root, fr)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	t.Cleanup(w.Close)
	orig := openWorkspace
	openWorkspace = func() (*workspace.Workspace, error) { return w, nil }
	t.Cleanup(func() { openWorkspace = orig })

	state := telemetrycfg.NewRunState(telemetrycfg.Resolution{})
	state.SetTraceID(testTraceID) // what the root pn.verb span setup does
	var out, errBuf bytes.Buffer
	cmd := newRootCmd("20260101-test000")
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"workspace", "update", "--in-place"})
	if err := cmd.ExecuteContext(telemetrycfg.WithRunState(context.Background(), state)); err != nil {
		t.Fatalf("update: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(stateHome, "pn", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("bad JSON line %q: %v", ln, err)
		}
		kind, _ := rec["kind"].(string)
		tid, has := rec["trace_id"]
		switch kind {
		case "run_start", "run_end":
			seen[kind] = true
			if tid != testTraceID {
				t.Errorf("%s trace_id = %v; want %s", kind, tid, testTraceID)
			}
		default:
			if has {
				t.Errorf("%s must not carry trace_id", kind)
			}
		}
	}
	if !seen["run_start"] || !seen["run_end"] {
		t.Fatalf("expected run_start and run_end, saw %v", seen)
	}
	if strings.Contains(out.String(), testTraceID) {
		t.Error("trace id leaked into stdout")
	}
}

// TestTomlEnabledFalse_OffPathIsSilentAndConnectionFree: with telemetry.toml
// `enabled = false` (and an endpoint present in both the file and the
// environment, pointing at a live listener) a pn run prints nothing about
// telemetry, even under -v, records no trace id and opens zero connections.
func TestTomlEnabledFalse_OffPathIsSilentAndConnectionFree(t *testing.T) {
	home := isolateTelemetryEnv(t)
	withTraceCmd(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	conns := make(chan struct{}, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- struct{}{}
			_ = c.Close()
		}
	}()
	ep := "http://" + ln.Addr().String()
	dir := filepath.Join(home, ".config", "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "telemetry.toml"), []byte("enabled = false\nendpoint = \""+ep+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", ep)

	out, errOut, err := run(t, "-v", "tracecmd")
	if err != nil {
		t.Fatal(err)
	}
	if out != "payload\n" {
		t.Errorf("stdout = %q", out)
	}
	if strings.Contains(errOut, "telemetry") || strings.Contains(errOut, "collector") {
		t.Errorf("off path must print no telemetry output, got stderr %q", errOut)
	}
	select {
	case <-conns:
		t.Error("off path opened a connection to the configured endpoint")
	default:
	}
}
