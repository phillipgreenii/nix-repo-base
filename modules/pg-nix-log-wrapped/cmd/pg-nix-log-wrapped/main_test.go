package main

// Process-level tests: the real wrapper (this test binary re-executed under
// the name "pg-nix-log-wrapped") runs a fake nix (the same binary, copied and
// symlinked as "fake-*"). Nothing here talks to a real network or a real OTel
// stack: collectors are loopback httptest servers.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

var (
	fakes   *fakecmd.Dir
	wrapper string // path of this test binary
)

func TestMain(m *testing.M) {
	if fakecmd.Run() {
		return
	}
	if filepath.Base(os.Args[0]) == "pg-nix-log-wrapped" {
		main()
		return
	}
	var err error
	if wrapper, err = os.Executable(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if fakes, err = fakecmd.NewDir(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	fakes.Close()
	os.Exit(code)
}

func golden(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "internal", "nixlog", "testdata", name))
	return p
}

// collector is a loopback OTLP/HTTP sink.
type collector struct {
	srv *httptest.Server
	mu  sync.Mutex
	// per request: the spans / metric batches received, with their resources.
	spans   []*tracepb.Span
	traceRs []*tracepb.ResourceSpans
	metrics []*metricspb.ResourceMetrics
	// refuseTraces is how many leading /v1/traces POSTs are answered with
	// HTTP 503 (the collector memory limiter's refusal) before accepting.
	refuseTraces int
	// tracePosts counts every /v1/traces POST, refused or accepted.
	tracePosts int
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			if gz, err := gzip.NewReader(r.Body); err == nil {
				body = gz
			}
		}
		raw, _ := io.ReadAll(body)
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.URL.Path {
		case "/v1/traces":
			c.tracePosts++
			if c.tracePosts <= c.refuseTraces {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			req := &coltrace.ExportTraceServiceRequest{}
			if proto.Unmarshal(raw, req) == nil {
				for _, rs := range req.ResourceSpans {
					c.traceRs = append(c.traceRs, rs)
					for _, ss := range rs.ScopeSpans {
						c.spans = append(c.spans, ss.Spans...)
					}
				}
			}
		case "/v1/metrics":
			req := &colmetrics.ExportMetricsServiceRequest{}
			if proto.Unmarshal(raw, req) == nil {
				c.metrics = append(c.metrics, req.ResourceMetrics...)
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// setRefuse sets how many leading /v1/traces POSTs are refused with 503.
func (c *collector) setRefuse(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuseTraces = n
}

func (c *collector) spansNamed(name string) []*tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*tracepb.Span
	for _, s := range c.spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func attrStr(attrs []*commonpb.KeyValue, key string) (string, bool) {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value.GetStringValue(), true
		}
	}
	return "", false
}

// env builds an explicit environment: nothing leaks in from the test runner.
func env(t *testing.T, extra ...string) (e []string, home string) {
	t.Helper()
	home = t.TempDir()
	defaults := []string{
		"PATH=" + fakes.Path() + string(os.PathListSeparator) + "/usr/bin:/bin",
		"HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"PG_NIX_LOG_FLUSH_TIMEOUT=500ms",
	}
	// Later entries override defaults of the same name (getenv takes the FIRST).
	overridden := map[string]bool{}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		overridden[k] = true
	}
	for _, kv := range defaults {
		if k, _, _ := strings.Cut(kv, "="); !overridden[k] {
			e = append(e, kv)
		}
	}
	return append(e, extra...), home
}

type result struct {
	stdout, stderr string
	code           int
	err            error
}

func wrapperCmd(env []string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.Command(wrapper, args...)
	cmd.Args[0] = "pg-nix-log-wrapped"
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	return cmd, &out, &errb
}

func runWrapper(t *testing.T, env []string, args ...string) result {
	t.Helper()
	fakes.Command(t, "fake-nix")
	cmd, out, errb := wrapperCmd(env, args...)
	err := cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	}
	return result{out.String(), errb.String(), code, err}
}

func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}

func TestPassThroughOfStreamsAndExitCodes(t *testing.T) {
	spec := fakecmd.Spec{Stdout: "the-stdout\n", Stderr: "the-stderr\n", Exit: 5}
	dead := "http://" + deadAddr(t)
	col := newCollector(t)

	for name, e := range map[string][]string{
		"disabled (real exec)":    {"PG_NIX_LOG_DISABLE=1", "OTEL_EXPORTER_OTLP_ENDPOINT=" + col.srv.URL},
		"no endpoint (real exec)": {},
		"dead collector":          {"OTEL_EXPORTER_OTLP_ENDPOINT=" + dead},
		"live collector":          {"OTEL_EXPORTER_OTLP_ENDPOINT=" + col.srv.URL},
	} {
		t.Run(name, func(t *testing.T) {
			base, _ := env(t, spec.Env())
			res := runWrapper(t, append(base, e...), "fake-nix", "build")
			if res.code != 5 {
				t.Errorf("exit = %d, want 5 (err %v)", res.code, res.err)
			}
			// Byte-identical to running the command directly: the wrapper adds
			// NOTHING to either stream, including OTel export errors.
			if res.stdout != "the-stdout\n" || res.stderr != "the-stderr\n" {
				t.Errorf("stdout=%q stderr=%q", res.stdout, res.stderr)
			}
		})
	}
}

func TestFailOpenExecsWithUnmodifiedEnvironment(t *testing.T) {
	e, home := env(t, "PG_NIX_LOG_DISABLE=1", "NIX_CONFIG=ambient = 1")
	dump := filepath.Join(home, "dump.json")
	e = append(e, fakecmd.Spec{Dump: dump}.Env())
	res := runWrapper(t, e, "fake-nix")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	b, _ := os.ReadFile(dump)
	if !strings.Contains(string(b), `"nix_config":"ambient = 1"`) || strings.Contains(string(b), "json-log-path") {
		t.Errorf("fail-open modified NIX_CONFIG: %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, "state")); err == nil {
		t.Error("fail-open created the log directory")
	}
}

func TestCommandNotFoundMatchesExec(t *testing.T) {
	e, _ := env(t, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1")
	res := runWrapper(t, e, "fake-no-such-command")
	if res.code != 127 || res.stderr != "exec: \"fake-no-such-command\": executable file not found in $PATH\n" || res.stdout != "" {
		t.Errorf("code=%d stderr=%q", res.code, res.stderr)
	}
}

func TestSigkilledChildGives137(t *testing.T) {
	e, _ := env(t, "OTEL_EXPORTER_OTLP_ENDPOINT=http://"+deadAddr(t), fakecmd.Spec{KillSelf: 9, Fixture: golden("killed-mid-build.jsonl")}.Env())
	res := runWrapper(t, e, "fake-nix", "build")
	if res.code != 137 || res.stderr != "" {
		t.Errorf("code=%d stderr=%q", res.code, res.stderr)
	}
}

// signalRun starts the wrapper around a fake that holds until signalled.
type signalRun struct {
	cmd             *exec.Cmd
	col             *collector
	signalLog, home string
	done            chan struct{}
	err             error
}

func startHeld(t *testing.T, spec fakecmd.Spec) *signalRun {
	t.Helper()
	return startHeldOn(t, spec, newCollector(t), "500ms")
}

// startHeldOn is startHeld against a caller-supplied collector and flush budget.
func startHeldOn(t *testing.T, spec fakecmd.Spec, col *collector, flush string) *signalRun {
	t.Helper()
	fakes.Command(t, "fake-nix")
	home := t.TempDir()
	spec.Ready = filepath.Join(home, "ready")
	spec.SignalLog = filepath.Join(home, "signals")
	spec.HoldForMS = 20000
	e := []string{
		"PATH=" + fakes.Path() + ":/usr/bin:/bin", "HOME=" + home, "XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + col.srv.URL, "PG_NIX_LOG_FLUSH_TIMEOUT=" + flush, spec.Env(),
	}
	cmd, _, _ := wrapperCmd(e, "fake-nix")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own group: tests can signal "the terminal's" group
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &signalRun{cmd: cmd, col: col, signalLog: spec.SignalLog, home: home, done: make(chan struct{})}
	go func() { r.err = cmd.Wait(); close(r.done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-r.done
	})
	waitFor(t, 20*time.Second, func() bool { _, err := os.Stat(spec.Ready); return err == nil }, "fake command to be ready")
	return r
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *signalRun) signals() []string {
	b, _ := os.ReadFile(r.signalLog)
	return strings.Fields(string(b))
}

func (r *signalRun) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(30 * time.Second):
		t.Fatal("wrapper did not exit")
	}
	return r.cmd.ProcessState.ExitCode()
}

// assertLogsFinalized checks a finished run left the retained .jsonl and
// deleted the scratch .raw, however the run ended.
func (r *signalRun) assertLogsFinalized(t *testing.T) {
	t.Helper()
	dir := filepath.Join(r.home, "state", "pn", "nix-logs")
	if raws, _ := filepath.Glob(filepath.Join(dir, "*.raw")); len(raws) != 0 {
		t.Errorf("scratch file left behind after the signal: %v", raws)
	}
	if logs, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(logs) != 1 {
		t.Errorf("retained logs = %v, want exactly one", logs)
	}
}

func TestSIGTERMAndSIGHUPAreForwardedExactlyOnce(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			r := startHeld(t, fakecmd.Spec{ExitOnSignal: 42})
			if err := syscall.Kill(r.cmd.Process.Pid, sig); err != nil { // to the wrapper ONLY
				t.Fatal(err)
			}
			if code := r.wait(t); code != 42 {
				t.Errorf("exit = %d, want the child's 42", code)
			}
			want := map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGHUP: "HUP"}[sig]
			if got := r.signals(); len(got) != 1 || got[0] != want {
				t.Errorf("child saw %v, want exactly [%s]", got, want)
			}
			r.assertLogsFinalized(t)
		})
	}
}

func TestSIGINTFromTheProcessGroupReachesChildExactlyOnce(t *testing.T) {
	r := startHeld(t, fakecmd.Spec{ExitOnSignal: 7, ExitOnINT: true})
	// What a terminal's Ctrl-C does: signal the whole foreground group.
	if err := syscall.Kill(-r.cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(t); code != 7 {
		t.Errorf("exit = %d, want 7 (the wrapper must survive INT and report the child's status)", code)
	}
	if got := r.signals(); len(got) != 1 || got[0] != "INT" {
		t.Errorf("child saw %v, want exactly one INT (group delivery, no forward)", got)
	}
	r.assertLogsFinalized(t)
}

func TestSIGINTToTheWrapperAloneIsNotForwarded(t *testing.T) {
	r := startHeld(t, fakecmd.Spec{ExitOnSignal: 9})
	if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	select {
	case <-r.done:
		t.Fatal("the wrapper died on SIGINT instead of waiting for its child")
	default:
	}
	if got := r.signals(); len(got) != 0 {
		t.Fatalf("SIGINT was forwarded: %v", got)
	}
	if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(t); code != 9 {
		t.Errorf("exit = %d", code)
	}
	if got := r.signals(); len(got) != 1 || got[0] != "TERM" {
		t.Errorf("child saw %v", got)
	}
}

// runningBuilds is the log a nix that is mid-build writes before it is killed:
// n derivations started (activity type 105, fields [drv, machine, round,
// rounds]) and none stopped. Nix prints no completion line for them and the
// spans export only at the end of a run, so after a signal the abort path is
// the only record of which derivations were running (pg2-sdbhh4.6).
func runningBuilds(names ...string) (lines []string, want []string) {
	for i, n := range names {
		drv := fmt.Sprintf("/nix/store/%032d-%s.drv", i+1, n)
		lines = append(lines, fmt.Sprintf(`{"action":"start","fields":[%q,"",1,1],"id":%d,"level":3,"parent":0,"text":"building '%s'","type":105}`, drv, 1000+i, drv))
		want = append(want, n)
	}
	return lines, want
}

// assertAbortedExport checks what the collector must hold after a run that
// was killed by signal sig (exit 128+sig) with the given builds still open:
// one failed nix.invocation, and one aborted nix.build per open build.
func assertAbortedExport(t *testing.T, col *collector, sig syscall.Signal, wantNames []string) {
	t.Helper()
	invs := col.spansNamed("nix.invocation")
	if len(invs) != 1 {
		t.Fatalf("collector received %d nix.invocation spans, want 1", len(invs))
	}
	inv, ia := invs[0], attrsOf(invs[0])
	if inv.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Errorf("nix.invocation status = %v, want error", inv.Status)
	}
	if got := ia["process.exit.code"].GetIntValue(); got != int64(128+int(sig)) {
		t.Errorf("process.exit.code = %d, want %d", got, 128+int(sig))
	}
	if got := ia["error.type"].GetStringValue(); got != "signal" {
		t.Errorf("nix.invocation error.type = %q, want signal", got)
	}
	got := map[string]int{}
	for _, b := range col.spansNamed("nix.build") {
		ba := attrsOf(b)
		if v := ba["error.type"].GetStringValue(); v != "aborted" {
			t.Errorf("nix.build %q error.type = %q, want aborted", ba["nix.drv.name"].GetStringValue(), v)
		}
		if b.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR {
			t.Errorf("nix.build %q status = %v, want error", ba["nix.drv.name"].GetStringValue(), b.Status)
		}
		if hex.EncodeToString(b.TraceId) != hex.EncodeToString(inv.TraceId) {
			t.Errorf("nix.build %q is not in the invocation's trace", ba["nix.drv.name"].GetStringValue())
		}
		got[ba["nix.drv.name"].GetStringValue()]++
	}
	if len(got) != len(wantNames) {
		t.Errorf("aborted nix.build spans by drv name = %v, want exactly one each of %v", got, wantNames)
	}
	for _, n := range wantNames {
		if got[n] != 1 {
			t.Errorf("nix.build spans named %q = %d, want 1 (all: %v)", n, got[n], got)
		}
	}
}

// A killed run must still say which derivations were running: the wrapper
// closes every open build as an aborted span and exports it, with the
// invocation, inside the flush budget. SIGTERM goes to the wrapper alone (the
// wrapper forwards it); SIGINT goes to the process GROUP, as a terminal
// Ctrl-C does (the wrapper catches it and does not forward).
func TestSignalledRunExportsAbortedBuildSpans(t *testing.T) {
	cases := []struct {
		name  string
		sig   syscall.Signal
		group bool
		spec  fakecmd.Spec
	}{
		{"SIGTERM to the wrapper", syscall.SIGTERM, false, fakecmd.Spec{DieOnSignal: true}},
		{"SIGINT to the process group", syscall.SIGINT, true, fakecmd.Spec{DieOnSignal: true, ExitOnINT: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, want := runningBuilds("alpha-1.0", "beta-2.1", "gamma-3")
			tc.spec.Lines = lines
			col := newCollector(t)
			r := startHeldOn(t, tc.spec, col, "500ms")
			start := time.Now()
			target := r.cmd.Process.Pid
			if tc.group {
				target = -target
			}
			if err := syscall.Kill(target, tc.sig); err != nil {
				t.Fatal(err)
			}
			wantCode := 128 + int(tc.sig)
			if code := r.wait(t); code != wantCode {
				t.Errorf("exit = %d, want %d (the child died of the signal)", code, wantCode)
			}
			// Signal to exit covers the drain, the finish and the bounded flush
			// (500ms budget plus the 500ms the shutdown context adds).
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("signal to wrapper exit took %v, want well inside the flush budget", d)
			}
			if got := r.signals(); len(got) != 1 || got[0] != map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGINT: "INT"}[tc.sig] {
				t.Errorf("child saw %v, want exactly one %v", got, tc.sig)
			}
			assertAbortedExport(t, col, tc.sig, want)
			r.assertLogsFinalized(t)
		})
	}
}

// The 503 behavior, pinned (README "Spans can be fewer than log files"): the
// exporter retries a refusal inside the flush budget, so a refusal that ends
// in time loses nothing even on the signal path, and a refusal that outlasts
// the budget drops the WHOLE trace batch (the invocation and every aborted
// build) without changing the exit status or delaying exit past the budget.
func TestCollector503OnASignalledRun(t *testing.T) {
	t.Run("one refusal is retried and nothing is lost", func(t *testing.T) {
		lines, want := runningBuilds("alpha-1.0", "beta-2.1")
		col := newCollector(t)
		col.setRefuse(1)
		r := startHeldOn(t, fakecmd.Spec{DieOnSignal: true, Lines: lines}, col, "2s")
		if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if code := r.wait(t); code != 143 {
			t.Errorf("exit = %d, want 143", code)
		}
		col.mu.Lock()
		posts := col.tracePosts
		col.mu.Unlock()
		if posts < 2 {
			t.Errorf("collector saw %d /v1/traces POST(s), want >= 2 (a retry after the 503)", posts)
		}
		assertAbortedExport(t, col, syscall.SIGTERM, want)
	})
	t.Run("a refusal that outlasts the budget drops the whole trace batch", func(t *testing.T) {
		lines, _ := runningBuilds("alpha-1.0", "beta-2.1")
		col := newCollector(t)
		col.setRefuse(1 << 30)
		r := startHeldOn(t, fakecmd.Spec{DieOnSignal: true, Lines: lines}, col, "500ms")
		start := time.Now()
		if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if code := r.wait(t); code != 143 {
			t.Errorf("exit = %d, want 143 (a dropped export never changes the exit status)", code)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("wrapper took %v to give up, want inside the flush budget", d)
		}
		col.mu.Lock()
		posts := col.tracePosts
		col.mu.Unlock()
		if posts < 2 {
			t.Errorf("collector saw %d /v1/traces POST(s), want >= 2 (retried until the budget ended)", posts)
		}
		if n := len(col.spansNamed("nix.invocation")) + len(col.spansNamed("nix.build")); n != 0 {
			t.Errorf("collector holds %d spans, want 0: the refused batch is dropped whole", n)
		}
		r.assertLogsFinalized(t) // the retained log is then the only record
	})
}

func TestChildDoesNotInheritIgnoredSIGINT(t *testing.T) {
	e, home := env(t, "OTEL_EXPORTER_OTLP_ENDPOINT="+newCollector(t).srv.URL)
	dump := filepath.Join(home, "dump.json")
	e = append(e, fakecmd.Spec{Dump: dump}.Env())
	if res := runWrapper(t, e, "fake-nix"); res.code != 0 {
		t.Fatalf("exit %d", res.code)
	}
	b, _ := os.ReadFile(dump)
	if !strings.Contains(string(b), `"int_ignored":false`) {
		t.Errorf("child has SIGINT ignored (a Ctrl-C would be swallowed): %s", b)
	}
}

func attrsOf(s *tracepb.Span) map[string]*commonpb.AnyValue {
	m := map[string]*commonpb.AnyValue{}
	for _, kv := range s.Attributes {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestOTLPEndToEnd(t *testing.T) {
	col := newCollector(t)
	const tid, sid = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	e, home := env(t, fakecmd.Spec{Fixture: golden("failed-build-keep-going.jsonl"), Exit: 1}.Env())
	run := func() {
		t.Helper()
		res := runWrapper(t, e, "--otlp-endpoint", col.srv.URL, "--traceparent", "00-"+tid+"-"+sid+"-01", "fake-nix", "build")
		if res.code != 1 || res.stderr != "" || res.stdout != "" {
			t.Fatalf("code=%d stderr=%q stdout=%q", res.code, res.stderr, res.stdout)
		}
	}
	run()
	run() // two processes of the same service

	// Resource: service.name set in code; nothing unique per process.
	col.mu.Lock()
	defer col.mu.Unlock()
	if len(col.traceRs) == 0 || len(col.metrics) == 0 {
		t.Fatalf("collector got traces=%d metrics=%d", len(col.traceRs), len(col.metrics))
	}
	for _, rs := range col.traceRs {
		if v, _ := attrStr(rs.Resource.Attributes, "service.name"); v != "pg-nix-log-wrapped" {
			t.Errorf("service.name = %q", v)
		}
		for _, kv := range rs.Resource.Attributes {
			if kv.Key == "service.instance.id" {
				t.Error("service.instance.id would split Prometheus series per run")
			}
		}
	}

	var invs, builds, failedBuilds int
	for _, s := range col.spans {
		switch s.Name {
		case "nix.invocation":
			invs++
			if hex.EncodeToString(s.TraceId) != tid || hex.EncodeToString(s.ParentSpanId) != sid {
				t.Errorf("invocation not linked: trace=%x parent=%x", s.TraceId, s.ParentSpanId)
			}
			if s.Kind != tracepb.Span_SPAN_KIND_INTERNAL || s.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR {
				t.Errorf("kind=%v status=%v", s.Kind, s.Status)
			}
			if v := attrsOf(s)["process.exit.code"]; v.GetIntValue() != 1 {
				t.Errorf("process.exit.code = %v", v)
			}
		case "nix.build":
			builds++
			if s.Status.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
				failedBuilds++
			}
		}
	}
	if invs != 2 || builds != 8 || failedBuilds != 4 {
		t.Errorf("invocations=%d builds=%d failed=%d, want 2/8/4", invs, builds, failedBuilds)
	}

	// Metrics: DELTA temporality, units in seconds, and one process's counts
	// are its own increments (2 built, 2 failed per run), not a running total.
	built, failed := map[int]int64{}, map[int]int64{}
	var sawHistUnit, sawCounterUnit bool
	for i, rm := range col.metrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch m.Name {
				case "nix.derivations":
					sum := m.GetSum()
					if sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
						t.Errorf("nix.derivations temporality = %v", sum.AggregationTemporality)
					}
					sawCounterUnit = m.Unit == "{derivation}"
					for _, dp := range sum.DataPoints {
						switch v, _ := attrStr(dp.Attributes, "outcome"); v {
						case "built":
							built[i] += dp.GetAsInt()
						case "failed":
							failed[i] += dp.GetAsInt()
						}
					}
				case "nix.invocation.duration", "nix.build.duration":
					h := m.GetHistogram()
					if h.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
						t.Errorf("%s temporality = %v", m.Name, h.AggregationTemporality)
					}
					sawHistUnit = m.Unit == "s"
					if len(h.DataPoints) > 0 && len(h.DataPoints[0].ExplicitBounds) == 0 {
						t.Errorf("%s has no explicit bucket boundaries", m.Name)
					}
				}
			}
		}
	}
	if !sawHistUnit || !sawCounterUnit {
		t.Errorf("units: histogram s=%v counter {derivation}=%v", sawHistUnit, sawCounterUnit)
	}
	var totalBuilt, totalFailed int64
	for _, v := range built {
		totalBuilt += v
	}
	for _, v := range failed {
		totalFailed += v
	}
	if totalBuilt != 4 || totalFailed != 4 {
		t.Errorf("summed deltas built=%d failed=%d, want 4/4 over two runs", totalBuilt, totalFailed)
	}
	for i, v := range built {
		if v > 2 {
			t.Errorf("export %d reports built=%d: cumulative, not delta", i, v)
		}
	}
	_ = home
}

// A single transient 503 from the collector (its memory limiter refuses data
// in bursts) must not drop the span batch: the exporter retries inside the
// flush budget, so the nix.invocation span still arrives.
func TestTransient503OnTracesIsRetriedInsideTheFlushBudget(t *testing.T) {
	col := newCollector(t)
	col.mu.Lock()
	col.refuseTraces = 1
	col.mu.Unlock()
	e, _ := env(t, fakecmd.Spec{Fixture: golden("failed-build-keep-going.jsonl"), Exit: 1}.Env(), "PG_NIX_LOG_FLUSH_TIMEOUT=2s")
	res := runWrapper(t, e, "--otlp-endpoint", col.srv.URL, "fake-nix", "build")
	if res.code != 1 || res.stderr != "" {
		t.Fatalf("code=%d stderr=%q", res.code, res.stderr)
	}
	col.mu.Lock()
	posts := col.tracePosts
	col.mu.Unlock()
	if posts < 2 {
		t.Errorf("collector saw %d /v1/traces POST(s), want >= 2 (a retry after the 503)", posts)
	}
	if got := len(col.spansNamed("nix.invocation")); got != 1 {
		t.Errorf("collector received %d nix.invocation spans, want 1", got)
	}
}

func TestThreeHundredPlusActivitiesAllReachTheCollector(t *testing.T) {
	// 5000 substitutions exceed the SDK default queue (2048) by far: they all
	// arrive only because the wrapper sets a larger span queue (W-8).
	const n = 5000
	col := newCollector(t)
	home := t.TempDir()
	fixture := filepath.Join(home, "many.jsonl")
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `{"action":"start","fields":["/nix/store/%032d-p%d","https://cache.nixos.org"],"id":%d,"level":0,"parent":0,"text":"","type":108}`+"\n", i, i, i)
	}
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `{"action":"stop","id":%d}`+"\n", i)
	}
	if err := os.WriteFile(fixture, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _ := env(t, fakecmd.Spec{Fixture: fixture}.Env(), "PG_NIX_LOG_FLUSH_TIMEOUT=20s")
	res := runWrapper(t, e, "--otlp-endpoint", col.srv.URL, "--min-substitute-span=0", "fake-nix")
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("code=%d stderr=%q", res.code, res.stderr)
	}
	if got := len(col.spansNamed("nix.substitute")); got != n {
		t.Errorf("collector received %d of %d substitute spans", got, n)
	}
}

func TestCanaryRealNixBuildProducesABuildSpan(t *testing.T) {
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Skip("nix is not installed")
	}
	if os.Getenv("NIX_BUILD_TOP") != "" {
		t.Skip("inside a nix builder: no nested store operations")
	}
	col := newCollector(t)
	e, home := env(t, "OTEL_EXPORTER_OTLP_ENDPOINT="+col.srv.URL, "PATH="+filepath.Dir(nix)+":/usr/bin:/bin")
	expr := fmt.Sprintf(`derivation { name = "pgnlw-canary-%d"; system = builtins.currentSystem; builder = "/bin/sh"; args = [ "-c" "echo canary > $out" ]; }`, time.Now().UnixNano())
	res := runWrapper(t, e, "nix", "build", "--impure", "--no-link", "--extra-experimental-features", "nix-command flakes", "--expr", expr)
	if res.code != 0 {
		t.Skipf("nix build is not usable in this environment (exit %d): %s", res.code, res.stderr)
	}
	if got := len(col.spansNamed("nix.build")); got < 1 {
		t.Fatalf("a real nix build produced no nix.build span: nix's json-log-path format may have changed (home %s)", home)
	}
	if len(col.spansNamed("nix.invocation")) != 1 {
		t.Error("missing nix.invocation span")
	}
}

func TestPathShadowDoesNotRecurseThroughTheRealBinary(t *testing.T) {
	// The wrapper is its own PATH entry under the name of CMD (a shadow): it
	// must find the real command behind it, not exec itself forever.
	shadowDir := t.TempDir()
	if err := os.Symlink(wrapper, filepath.Join(shadowDir, "fake-nix")); err != nil {
		t.Fatal(err)
	}
	e, _ := env(t, fakecmd.Spec{Stdout: "real\n"}.Env(), "PG_NIX_LOG_DISABLE=1")
	e[0] = "PATH=" + shadowDir + string(os.PathListSeparator) + fakes.Path()
	fakes.Command(t, "fake-nix")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, wrapper, "fake-nix")
	cmd.Args[0] = "pg-nix-log-wrapped"
	cmd.Env = e
	out, err := cmd.Output()
	if err != nil || string(out) != "real\n" {
		t.Errorf("out=%q err=%v", out, err)
	}
}
