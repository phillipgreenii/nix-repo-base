package main

// Contract test between pn and the wrapper (bead pg2-sdbhh4.9). pn
// (modules/pn) and this module are separate Go modules and pn cannot import
// ParseArgs from internal/config, so the contract is a golden file: the argv pn
// produces for the user build step. pn's exec/wrap_test.go asserts its generated
// argv equals that file byte for byte; this test feeds the same file to
// config.ParseArgs and to the real wrapper binary.
//
// The file lives in pn's testdata. Each module's nix check builds from its own
// sources, so the wrapper check's source is widened with lib.fileset to include
// the file (flake.nix, pg-nix-log-wrapped-go-tests). This test MUST FAIL, never
// skip, when the file is missing: a skipped contract test is no contract.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
)

// pnWrapperArgvGolden is relative to this package's directory:
// modules/pg-nix-log-wrapped/cmd/pg-nix-log-wrapped -> modules/pn/....
const pnWrapperArgvGolden = "../../../pn/internal/exec/testdata/wrapper-user-build-argv.golden"

const (
	contractTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	contractTraceID     = "0af7651916cd43dd8448eb211c80319c"
	contractSpanID      = "b7ad6b7169203331"
)

// loadPNArgv reads the golden and substitutes the three run-specific values.
func loadPNArgv(t *testing.T, endpoint, logDir string) []string {
	t.Helper()
	data, err := os.ReadFile(pnWrapperArgvGolden)
	if err != nil {
		t.Fatalf("the shared pn argv golden is missing (%v): the nix check must include modules/pn/internal/exec/testdata/wrapper-user-build-argv.golden in this module's source (flake.nix, lib.fileset); this test never skips", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	sub := strings.NewReplacer("{traceparent}", contractTraceparent, "{otlp-endpoint}", endpoint, "{log-dir}", logDir)
	for i, l := range lines {
		lines[i] = sub.Replace(l)
	}
	return lines
}

// config.ParseArgs accepts the argv exactly as pn builds it and recovers the
// flags and CMD pn meant.
func TestPNUserBuildArgvParses(t *testing.T) {
	argv := loadPNArgv(t, "http://127.0.0.1:4318", "/state/pn/nix-logs")
	o, err := config.ParseArgs(argv)
	if err != nil {
		t.Fatalf("ParseArgs rejects the argv pn produces: %v\nargv: %q", err, argv)
	}
	if o.Traceparent != contractTraceparent || o.OTLPEndpoint != "http://127.0.0.1:4318" || o.LogDir != "/state/pn/nix-logs" {
		t.Errorf("flags not recovered: %+v", o)
	}
	wantCmd := []string{"darwin-rebuild", "build", "--flake", "/x/leaf#host", "--override-input", "dep-input", "git+file:///x/dep"}
	if !reflect.DeepEqual(o.Cmd, wantCmd) {
		t.Errorf("CMD = %q, want %q (everything after the first --, untouched)", o.Cmd, wantCmd)
	}
	if o.Check || o.Help {
		t.Errorf("unexpected mode flags: %+v", o)
	}
}

// The real wrapper binary accepts the same argv: it honours --log-dir and
// --traceparent, and runs CMD with exactly its own arguments. The one
// substitution is CMD's name (darwin-rebuild -> a fake command, which must carry
// the fake- prefix of this package's re-exec harness); everything else is the
// golden.
func TestPNUserBuildArgvRunsTheRealWrapper(t *testing.T) {
	col := newCollector(t)
	e, home := env(t)
	logDir := filepath.Join(home, "state", "pn", "nix-logs")
	argv := loadPNArgv(t, col.srv.URL, logDir)
	o, err := config.ParseArgs(argv)
	if err != nil {
		t.Fatal(err)
	}
	if o.Cmd[0] != "darwin-rebuild" {
		t.Fatalf("CMD[0] = %q, want darwin-rebuild", o.Cmd[0])
	}
	dump := filepath.Join(home, "dump.json")
	e = append(e, fakecmd.Spec{Fixture: golden("local-build.jsonl"), Dump: dump}.Env())
	argv[len(argv)-len(o.Cmd)] = "fake-nix" // CMD[0] is the first argv element after --

	res := runWrapper(t, e, argv...)
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("exit=%d stderr=%q err=%v", res.code, res.stderr, res.err)
	}

	// CMD received exactly its own arguments.
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the command never ran: %v", err)
	}
	var d struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(b, &d); err != nil || len(d.Argv) < 1 {
		t.Fatalf("bad dump %q: %v", b, err)
	}
	if !reflect.DeepEqual(d.Argv[1:], o.Cmd[1:]) {
		t.Errorf("CMD argv = %q, want %q", d.Argv[1:], o.Cmd[1:])
	}

	// --log-dir honoured: the retained log is in it.
	if logs, _ := filepath.Glob(filepath.Join(logDir, "*.jsonl")); len(logs) != 1 {
		t.Errorf("retained logs in %s = %v, want exactly one (--log-dir not honoured)", logDir, logs)
	}

	// --traceparent honoured and the endpoint reached: the invocation span is a
	// child of the span pn named.
	col.mu.Lock()
	defer col.mu.Unlock()
	found := false
	for _, s := range col.spans {
		if s.Name != "nix.invocation" {
			continue
		}
		found = true
		if hex.EncodeToString(s.TraceId) != contractTraceID || hex.EncodeToString(s.ParentSpanId) != contractSpanID {
			t.Errorf("nix.invocation not linked to the traceparent: trace=%x parent=%x", s.TraceId, s.ParentSpanId)
		}
	}
	if !found {
		t.Error("no nix.invocation span reached the collector (--otlp-endpoint not honoured)")
	}
}
