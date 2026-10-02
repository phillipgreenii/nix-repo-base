package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

var fakes *fakecmd.Dir

func TestMain(m *testing.M) {
	if fakecmd.Run() {
		return
	}
	d, err := fakecmd.NewDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakes = d
	code := m.Run()
	d.Close()
	os.Exit(code)
}

func golden(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "nixlog", "testdata", name))
	return p
}

// mem is an in-memory Telemetry factory.
type mem struct {
	mu     sync.Mutex
	calls  int
	cfg    otelsetup.Config
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	fail   error
}

func newMem() *mem {
	return &mem{spans: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader()}
}

func (m *mem) factory(_ context.Context, cfg otelsetup.Config, _ ...otelsetup.Option) (Telemetry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.cfg = cfg
	if m.fail != nil {
		return Telemetry{}, m.fail
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(m.spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(m.reader))
	return Telemetry{Tracer: tp.Tracer("t"), Meter: mp.Meter("m"), Shutdown: func(context.Context) error { return nil }}, nil
}

func (m *mem) span(t *testing.T, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range m.spans.Ended() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no %q span among %v", name, spanNames(m.spans.Ended()))
	return nil
}

func spanNames(ss []sdktrace.ReadOnlySpan) []string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name())
	}
	return n
}

func attrOf(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func (m *mem) counter(t *testing.T, outcome string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := m.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "nix.derivations" {
				continue
			}
			for _, dp := range mt.Data.(metricdata.Sum[int64]).DataPoints {
				if v, ok := dp.Attributes.Value("outcome"); ok && v.AsString() == outcome {
					return dp.Value
				}
			}
		}
	}
	return 0
}

func (m *mem) hasMetric(t *testing.T, name string) bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := m.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name == name {
				return true
			}
		}
	}
	return false
}

type exe struct {
	path string
	argv []string
	env  []string
}

// rig bundles a fully injected Deps and everything a test inspects.
type rig struct {
	d                   Deps
	mem                 *mem
	stdout, stderr      *bytes.Buffer
	childOut, childErr  *bytes.Buffer
	execs               []exe
	home, state, dumpTo string
	logDir              string
}

func newRig(t *testing.T, spec fakecmd.Spec, extraEnv ...string) *rig {
	t.Helper()
	home := t.TempDir()
	r := &rig{
		mem: newMem(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		childOut: &bytes.Buffer{}, childErr: &bytes.Buffer{},
		home: home, state: filepath.Join(home, "state"), dumpTo: filepath.Join(home, "dump.json"),
	}
	r.logDir = filepath.Join(r.state, "pn", "nix-logs")
	spec.Dump = r.dumpTo
	self, _ := os.Executable()
	r.d = Deps{
		Environ: append([]string{
			"PATH=" + fakes.Path() + string(os.PathListSeparator) + "/usr/bin:/bin",
			"HOME=" + home, "XDG_STATE_HOME=" + r.state,
			"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
			spec.Env(),
		}, extraEnv...),
		Euid: 501, Pid: 4242, Home: home, Executable: self, Version: "test",
		Stdout: r.stdout, Stderr: r.stderr,
		ChildStdout: r.childOut, ChildStderr: r.childErr,
		Now: time.Now,
		Exec: func(path string, argv, env []string) error {
			r.execs = append(r.execs, exe{path, argv, env})
			return nil
		},
		NewTelemetry: r.mem.factory,
		ReadFile:     os.ReadFile,
		Dial:         func(string, time.Duration) error { return nil },
		PollInterval: 2 * time.Millisecond,
		FlushTimeout: 300 * time.Millisecond,
	}
	fakes.Command(t, "fake-nix")
	return r
}

func (r *rig) setEnv(key, val string) {
	r.d.Environ = append(filterEnv(r.d.Environ, key), key+"="+val)
}

func (r *rig) run(args ...string) int {
	r.d.Args = args
	return Run(r.d)
}

func (r *rig) dump(t *testing.T) (nixConfig string, argv []string) {
	t.Helper()
	b, err := os.ReadFile(r.dumpTo)
	if err != nil {
		t.Fatalf("fake command did not run: %v", err)
	}
	var v struct {
		Argv      []string `json:"argv"`
		NixConfig string   `json:"nix_config"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.NixConfig, v.Argv
}

func (r *rig) logFiles(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(r.logDir, "*.jsonl"))
	return m
}

func TestObservedRunProducesSpansMetricsAndKeepsStreamsClean(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("local-build.jsonl"), Stdout: "out\n", Stderr: "err\n"})
	if code := r.run("fake-nix", "build", "--no-link", "."); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, r.stderr.String())
	}
	if len(r.execs) != 0 || r.mem.calls != 1 {
		t.Fatalf("execs=%d telemetry calls=%d", len(r.execs), r.mem.calls)
	}
	if r.mem.cfg.ServiceName != "pg-nix-log-wrapped" || r.mem.cfg.Endpoint != "http://127.0.0.1:4318" {
		t.Errorf("cfg = %+v", r.mem.cfg)
	}
	// CMD's bytes pass through untouched; the wrapper adds nothing.
	if r.childOut.String() != "out\n" || r.childErr.String() != "err\n" || r.stderr.Len() != 0 || r.stdout.Len() != 0 {
		t.Errorf("streams: out=%q err=%q wrapper stderr=%q", r.childOut, r.childErr, r.stderr)
	}

	inv := r.mem.span(t, "nix.invocation")
	if inv.SpanKind() != trace.SpanKindInternal || inv.Status().Code == codes.Error {
		t.Errorf("invocation kind=%v status=%v", inv.SpanKind(), inv.Status())
	}
	for key, want := range map[string]any{"command": "fake-nix", "process.exit.code": int64(0), "nix.plan.build": int64(1), "nix.plan.fetch": int64(0)} {
		v, ok := attrOf(inv, key)
		if !ok || v.AsInterface() != want {
			t.Errorf("invocation %s = %v (found %v), want %v", key, v.AsInterface(), ok, want)
		}
	}
	if v, ok := attrOf(inv, "nix.tailer.lag"); !ok || v.AsFloat64() < 0 {
		t.Errorf("nix.tailer.lag = %v (found %v)", v, ok)
	}
	if _, ok := attrOf(inv, "service.instance.id"); ok {
		t.Error("no per-process-unique attribute may be set")
	}
	b := r.mem.span(t, "nix.build")
	if b.Parent().SpanID() != inv.SpanContext().SpanID() {
		t.Error("nix.build is not a child of nix.invocation")
	}
	if v, _ := attrOf(b, "nix.drv.name"); v.AsString() != "kq2-slow-1790833925" {
		t.Errorf("drv name = %v", v)
	}
	if got := r.mem.counter(t, "built"); got != 1 {
		t.Errorf("built = %d", got)
	}
	if !r.mem.hasMetric(t, "nix.invocation.duration") {
		t.Error("invocation duration not recorded")
	}

	// The child got the log path through NIX_CONFIG and the file was kept.
	nc, argv := r.dump(t)
	files := r.logFiles(t)
	if len(files) != 1 || fakecmd.LogPath(nc) != files[0] {
		t.Fatalf("nix_config %q vs files %v", nc, files)
	}
	if strings.Join(argv[1:], " ") != "build --no-link ." {
		t.Errorf("argv = %v", argv)
	}
	want, _ := os.ReadFile(golden("local-build.jsonl"))
	if got, _ := os.ReadFile(files[0]); !bytes.Equal(got, want) {
		t.Error("raw log was not preserved byte for byte")
	}
	if fi, _ := os.Stat(files[0]); fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v", fi.Mode().Perm())
	}
}

func TestNonZeroExitIsPassedThroughAndMarksSpan(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Exit: 3})
	if code := r.run("fake-nix"); code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	inv := r.mem.span(t, "nix.invocation")
	if inv.Status().Code != codes.Error {
		t.Errorf("status = %v", inv.Status())
	}
	if v, _ := attrOf(inv, "process.exit.code"); v.AsInt64() != 3 {
		t.Errorf("exit code attr = %v", v)
	}
	if v, _ := attrOf(inv, "error.type"); v.AsString() == "" {
		t.Error("error.type missing")
	}
}

func TestKilledChildPassesThrough137AndClosesOpenSpansWithError(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("killed-mid-build.jsonl"), KillSelf: 9})
	if code := r.run("fake-nix", "build"); code != 137 {
		t.Fatalf("exit = %d, want 137 (128+SIGKILL)", code)
	}
	b := r.mem.span(t, "nix.build")
	if b.Status().Code != codes.Error {
		t.Errorf("open build must end with error status, got %v", b.Status())
	}
	if v, _ := attrOf(b, "error.type"); v.AsString() != "aborted" {
		t.Errorf("error.type = %v", v)
	}
	inv := r.mem.span(t, "nix.invocation")
	if v, _ := attrOf(inv, "process.exit.code"); v.AsInt64() != 137 || inv.Status().Code != codes.Error {
		t.Errorf("invocation = %v %v", v, inv.Status())
	}
	if !b.EndTime().Before(inv.EndTime().Add(time.Millisecond)) {
		t.Error("the invocation span must end after its activities")
	}
}

func TestFailedBuildGoldenEndToEnd(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("failed-build-keep-going.jsonl"), Exit: 1})
	if code := r.run("fake-nix"); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	failed, ok := 0, 0
	for _, s := range r.mem.spans.Ended() {
		if s.Name() != "nix.build" {
			continue
		}
		if s.Status().Code == codes.Error {
			failed++
		} else {
			ok++
		}
	}
	if failed != 2 || ok != 2 {
		t.Errorf("failed=%d ok=%d, want 2/2", failed, ok)
	}
	if r.mem.counter(t, "failed") != 2 || r.mem.counter(t, "built") != 2 {
		t.Errorf("failed=%d built=%d", r.mem.counter(t, "failed"), r.mem.counter(t, "built"))
	}
}

func TestCachedFetchCountsSubstitutedAndFiltersShortSpans(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("cached-fetch.jsonl")})
	// Real-time stamping makes every substitution far shorter than 250 ms:
	// counted in the metric, but without a span.
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := r.mem.counter(t, "substituted"); got != 4 {
		t.Errorf("substituted = %d, want 4", got)
	}
	for _, s := range r.mem.spans.Ended() {
		if s.Name() == "nix.substitute" {
			t.Errorf("a sub-threshold substitution kept its span")
		}
	}
	if v, _ := attrOf(r.mem.span(t, "nix.invocation"), "nix.plan.fetch"); v.AsInt64() != 4 {
		t.Errorf("plan fetch = %v", v)
	}
}

func TestSubstituteSpansWhenThresholdIsZero(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("cached-fetch.jsonl")})
	if code := r.run("--min-substitute-span=0", "fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	n := 0
	for _, s := range r.mem.spans.Ended() {
		if s.Name() == "nix.substitute" {
			n++
			if v, _ := attrOf(s, "server.address"); v.AsString() != "cache.nixos.org" {
				t.Errorf("server.address = %v", v)
			}
		}
	}
	if n != 4 {
		t.Errorf("substitute spans = %d", n)
	}
}

func TestPartialTrailingLineAndDroppedNoise(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("chatty-build.trimmed.jsonl"), Partial: `{"action":"start","id":9,"typ`})
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	names := spanNames(r.mem.spans.Ended())
	if len(names) != 2 {
		t.Errorf("spans = %v, want only nix.build and nix.invocation", names)
	}
}

func TestChunkedWritesAcrossLineBoundaries(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("local-build.jsonl"), ChunkBytes: 37, ChunkDelayMS: 1})
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if r.mem.counter(t, "built") != 1 {
		t.Error("a build split across read chunks was lost")
	}
}

func TestSizeCapMarksInvocationTruncated(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Fixture: golden("local-build.jsonl")})
	r.d.LogSizeCap = 2000
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	v, ok := attrOf(r.mem.span(t, "nix.invocation"), "nix.log.truncated")
	if !ok || !v.AsBool() {
		t.Errorf("nix.log.truncated = %v (found %v)", v, ok)
	}
}

func TestTraceparentLinking(t *testing.T) {
	const tid, sid = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	tp := "00-" + tid + "-" + sid + "-01"

	linked := func(s sdktrace.ReadOnlySpan) bool {
		return s.Parent().TraceID().String() == tid && s.Parent().SpanID().String() == sid && s.SpanContext().TraceID().String() == tid
	}
	t.Run("flag", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{}, "TRACEPARENT=00-11111111111111111111111111111111-2222222222222222-01")
		r.run("--traceparent", tp, "fake-nix")
		if !linked(r.mem.span(t, "nix.invocation")) {
			t.Error("--traceparent must win and link the invocation")
		}
	})
	t.Run("env", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{}, "TRACEPARENT="+tp)
		r.run("fake-nix")
		if !linked(r.mem.span(t, "nix.invocation")) {
			t.Error("TRACEPARENT env not honoured")
		}
	})
	t.Run("none starts a fresh root", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{})
		r.run("fake-nix")
		if r.mem.span(t, "nix.invocation").Parent().IsValid() {
			t.Error("expected a root span")
		}
	})
	for _, bad := range []string{"garbage", "00-xyz", "00-" + tid + "-" + sid, "ff-" + tid + "-" + sid + "-01", ""} {
		t.Run("malformed:"+bad, func(t *testing.T) {
			r := newRig(t, fakecmd.Spec{})
			if code := r.run("--traceparent", bad, "fake-nix"); code != 0 {
				t.Fatalf("exit = %d", code)
			}
			if r.mem.span(t, "nix.invocation").Parent().IsValid() {
				t.Error("a malformed traceparent must yield a fresh root, not an error")
			}
			if r.stderr.Len() != 0 || len(r.execs) != 0 {
				t.Errorf("must stay silent and observed: stderr=%q execs=%d", r.stderr, len(r.execs))
			}
		})
	}
}

func TestAmbientNixConfigIsKeptAndOurLineComesLast(t *testing.T) {
	r := newRig(t, fakecmd.Spec{}, "NIX_CONFIG=experimental-features = nix-command\njson-log-path = not/absolute/and/invalid")
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	nc, _ := r.dump(t)
	lines := strings.Split(nc, "\n")
	if len(lines) != 3 || lines[0] != "experimental-features = nix-command" || lines[1] != "json-log-path = not/absolute/and/invalid" {
		t.Fatalf("ambient lines changed: %q", nc)
	}
	if got := fakecmd.LogPath(nc); got != r.logFiles(t)[0] {
		t.Errorf("effective json-log-path %q is not ours", got)
	}
}

func TestChildEnv(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"unset", []string{"A=1"}, "json-log-path = /l/x.jsonl"},
		{"empty", []string{"NIX_CONFIG="}, "json-log-path = /l/x.jsonl"},
		{"no trailing newline", []string{"NIX_CONFIG=a = b"}, "a = b\njson-log-path = /l/x.jsonl"},
		{"trailing newline", []string{"NIX_CONFIG=a = b\n"}, "a = b\njson-log-path = /l/x.jsonl"},
		{"duplicates collapse to the first", []string{"NIX_CONFIG=first", "NIX_CONFIG=second"}, "first\njson-log-path = /l/x.jsonl"},
	}
	for _, c := range cases {
		got := ChildEnv(c.in, "/l/x.jsonl")
		var nix []string
		for _, kv := range got {
			if strings.HasPrefix(kv, "NIX_CONFIG=") {
				nix = append(nix, strings.TrimPrefix(kv, "NIX_CONFIG="))
			}
		}
		if len(nix) != 1 || nix[0] != c.want {
			t.Errorf("%s: NIX_CONFIG = %q, want %q", c.name, nix, c.want)
		}
		if got[0] != "A=1" && c.name == "unset" {
			t.Errorf("other env lost: %v", got)
		}
	}
}

func TestFailOpenWhenDisabledOrNoEndpoint(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		drop bool // remove the endpoint from env
	}{
		{"PG_NIX_LOG_DISABLE", []string{"PG_NIX_LOG_DISABLE=1"}, false},
		{"OTEL_SDK_DISABLED", []string{"OTEL_SDK_DISABLED=true"}, false},
		{"no endpoint", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, fakecmd.Spec{}, c.env...)
			if c.drop {
				r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
			}
			before := append([]string(nil), r.d.Environ...)
			if code := r.run("fake-nix", "build", "--", "x"); code != 0 {
				t.Fatalf("code = %d", code)
			}
			if len(r.execs) != 1 {
				t.Fatalf("execs = %d, want 1", len(r.execs))
			}
			e := r.execs[0]
			if e.path != filepath.Join(fakes.Path(), "fake-nix") || strings.Join(e.argv, " ") != "fake-nix build -- x" {
				t.Errorf("exec %q %q", e.path, e.argv)
			}
			if strings.Join(e.env, "\x00") != strings.Join(before, "\x00") {
				t.Error("fail-open must exec CMD with an UNMODIFIED environment")
			}
			if r.mem.calls != 0 || r.stderr.Len() != 0 {
				t.Errorf("telemetry calls=%d stderr=%q", r.mem.calls, r.stderr)
			}
			if _, err := os.Stat(r.state); err == nil {
				t.Error("fail-open must not create the log directory")
			}
		})
	}
}

func filterEnv(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func TestSetupFailuresFailOpenSilently(t *testing.T) {
	type setup func(t *testing.T, r *rig)
	cases := map[string]setup{
		"telemetry init error": func(_ *testing.T, r *rig) { r.mem.fail = errors.New("boom") },
		"log dir parent is a file": func(t *testing.T, r *rig) {
			if err := os.WriteFile(r.state, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"log dir is a dangling symlink": func(t *testing.T, r *rig) {
			if err := os.MkdirAll(filepath.Dir(r.logDir), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(r.home, "nowhere"), r.logDir); err != nil {
				t.Fatal(err)
			}
		},
		"log dir is a symlink to a file": func(t *testing.T, r *rig) {
			f := filepath.Join(r.home, "afile")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(r.logDir), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f, r.logDir); err != nil {
				t.Fatal(err)
			}
		},
		"log dir unwritable": func(t *testing.T, r *rig) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			if err := os.MkdirAll(r.logDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(r.logDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(r.logDir, 0o700) })
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, fakecmd.Spec{})
			set(t, r)
			if code := r.run("fake-nix"); code != 0 {
				t.Fatalf("code = %d", code)
			}
			if len(r.execs) != 1 {
				t.Fatalf("expected fail-open exec, got %d", len(r.execs))
			}
			if r.stderr.Len() != 0 {
				t.Errorf("fail-open must be silent, stderr = %q", r.stderr)
			}
			if _, err := os.Stat(r.dumpTo); err == nil {
				t.Error("the observed path ran CMD")
			}
		})
	}
}

func TestInvalidEndpointFailsOpenWithRealOtelsetup(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.d.NewTelemetry = DefaultDeps("x").NewTelemetry
	if code := r.run("--otlp-endpoint", "gopher://nope", "fake-nix"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(r.execs) != 1 || r.stderr.Len() != 0 {
		t.Errorf("execs=%d stderr=%q", len(r.execs), r.stderr)
	}
}

func TestDebugModeExplainsFailOpen(t *testing.T) {
	r := newRig(t, fakecmd.Spec{}, "PG_NIX_LOG_DISABLE=1", "PG_NIX_LOG_DEBUG=1")
	r.run("fake-nix")
	if !strings.Contains(r.stderr.String(), "fail open: PG_NIX_LOG_DISABLE") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestGarbageTomlFailsOpen(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
	if err := os.MkdirAll(filepath.Join(r.home, ".config", "pn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, ".config", "pn", "telemetry.toml"), []byte("\x00\xff garbage [[[ ="), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run("fake-nix")
	if len(r.execs) != 1 || r.stderr.Len() != 0 || r.mem.calls != 0 {
		t.Errorf("execs=%d stderr=%q calls=%d", len(r.execs), r.stderr, r.mem.calls)
	}
}

func TestTomlEndpointIsUsedWithInjectedHome(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
	if err := os.MkdirAll(filepath.Join(r.home, ".config", "pn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.home, ".config", "pn", "telemetry.toml"), []byte(`endpoint = "http://127.0.0.1:4999"`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.run("fake-nix")
	if r.mem.cfg.Endpoint != "http://127.0.0.1:4999" {
		t.Errorf("endpoint = %q", r.mem.cfg.Endpoint)
	}
}

func TestRootModeNeverUsesAUserDirectory(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.d.Euid = 0
	r.d.RootLogDir = filepath.Join(t.TempDir(), "var-log-stand-in")
	userDir := filepath.Join(r.home, "chosen-by-user")
	// Env is ignored for root; flags are not, but --log-dir must be.
	code := r.run("--otlp-endpoint", "http://127.0.0.1:4318", "--log-dir", userDir, "fake-nix")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if _, err := os.Stat(userDir); err == nil {
		t.Error("a root wrapper created the user-chosen log directory")
	}
	if _, err := os.Stat(r.state); err == nil {
		t.Error("a root wrapper used XDG_STATE_HOME")
	}
	if os.Geteuid() == 0 {
		if m, _ := filepath.Glob(filepath.Join(r.d.RootLogDir, "*.jsonl")); len(m) != 1 {
			t.Errorf("real root run must log into the root directory, got %v", m)
		}
		return
	}
	// Not really root: the stand-in directory is not root-owned, so root mode
	// refuses it and fails open rather than writing there.
	if len(r.execs) != 1 {
		t.Errorf("expected fail-open, got %d execs", len(r.execs))
	}
}

func TestRootModeIgnoresEnvEndpointAndTraceparent(t *testing.T) {
	r := newRig(t, fakecmd.Spec{}, "TRACEPARENT=00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	r.d.Euid = 0
	r.d.RootLogDir = filepath.Join(t.TempDir(), "x")
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if r.mem.calls != 0 {
		t.Error("root must not resolve an endpoint from env")
	}
	if len(r.execs) != 1 {
		t.Errorf("execs = %d", len(r.execs))
	}
}

func TestSweepRunsInOwnDirectoryOnly(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	if err := os.MkdirAll(r.logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(r.home, "other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	mk := func(dir, name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
		return p
	}
	stale, foreign := mk(r.logDir, "old.jsonl"), mk(other, "old.jsonl")
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale log in own directory survived")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("sweep touched a foreign directory")
	}
}

func TestConcurrentRunsGetDistinctLogFiles(t *testing.T) {
	const n = 8
	rigs := make([]*rig, n)
	shared := newRig(t, fakecmd.Spec{})
	for i := range rigs {
		rigs[i] = newRig(t, fakecmd.Spec{})
		rigs[i].d.Environ = filterEnv(rigs[i].d.Environ, "XDG_STATE_HOME")
		rigs[i].d.Environ = append(rigs[i].d.Environ, "XDG_STATE_HOME="+shared.state)
		rigs[i].d.Pid = 4242 // identical pid and (nearly) identical second
	}
	var wg sync.WaitGroup
	for _, r := range rigs {
		wg.Add(1)
		go func() { defer wg.Done(); r.run("fake-nix") }()
	}
	wg.Wait()
	files, _ := filepath.Glob(filepath.Join(shared.logDir, "*.jsonl"))
	if len(files) != n {
		t.Errorf("%d distinct files for %d runs", len(files), n)
	}
}

func TestCommandNotFoundAndNotExecutable(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	if code := r.run("fake-definitely-missing"); code != 127 {
		t.Errorf("code = %d, want 127", code)
	}
	if got, want := r.stderr.String(), "exec: \"fake-definitely-missing\": executable file not found in $PATH\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	r.stderr.Reset()
	if code := r.run(filepath.Join(r.home, "no-such")); code != 127 {
		t.Errorf("slash path: code = %d", code)
	}
	plain := filepath.Join(r.home, "plain")
	_ = os.WriteFile(plain, []byte("x"), 0o644)
	r.stderr.Reset()
	if code := r.run(plain); code != 126 || !strings.Contains(r.stderr.String(), "permission denied") {
		t.Errorf("non-executable: code=%d stderr=%q", code, r.stderr)
	}
	if len(r.execs) != 0 || r.mem.calls != 0 {
		t.Error("an unrunnable CMD must not reach telemetry or exec")
	}
}

func TestNoRecursionThroughPathShadow(t *testing.T) {
	real := fakes.Path()
	self := filepath.Join(t.TempDir(), "wrapper-stand-in")
	if err := os.WriteFile(self, []byte("#!/bin/sh\nexit 77\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shadow := t.TempDir()
	if err := os.Symlink(self, filepath.Join(shadow, "fake-nix")); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, fakecmd.Spec{})
	r.d.Executable = self
	r.d.Environ = append(filterEnv(r.d.Environ, "PATH"), "PATH="+shadow+string(os.PathListSeparator)+real)
	if code := r.run("fake-nix"); code != 0 {
		t.Fatalf("code = %d: the shadow (the wrapper itself) was run", code)
	}
	if _, err := os.Stat(r.dumpTo); err != nil {
		t.Error("the real command did not run")
	}
}

func TestUsageErrors(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	if code := r.run("--bogus", "fake-nix"); code != 2 || !strings.Contains(r.stderr.String(), "unknown flag") {
		t.Errorf("bad flag: %d %q", code, r.stderr)
	}
	r.stderr.Reset()
	if code := r.run(); code != 2 || !strings.Contains(r.stderr.String(), "no command given") {
		t.Errorf("no command: %d %q", code, r.stderr)
	}
	if code := r.run("--help"); code != 0 || !strings.Contains(r.stdout.String(), "usage: pg-nix-log-wrapped") {
		t.Errorf("help: %d %q", code, r.stdout)
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dial := DefaultDeps("x").Dial

	t.Run("healthy", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{})
		r.setEnv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
		r.d.Dial = dial
		if code := r.run("--check"); code != 0 {
			t.Fatalf("code = %d\n%s", code, r.stdout)
		}
		out := r.stdout.String()
		for _, want := range []string{"endpoint: " + srv.URL + " (from env)", "reachable: yes", "log-dir: " + r.logDir, "writable: yes"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in\n%s", want, out)
			}
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := l.Addr().String()
		_ = l.Close()
		r := newRig(t, fakecmd.Spec{})
		r.setEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
		r.d.Dial = dial
		if code := r.run("--check"); code != 1 || !strings.Contains(r.stdout.String(), "reachable: no") {
			t.Errorf("code=%d out=%s", code, r.stdout)
		}
	})
	t.Run("no endpoint", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{})
		r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
		if code := r.run("--check"); code != 1 || !strings.Contains(r.stdout.String(), "none resolved") {
			t.Errorf("code=%d out=%s", code, r.stdout)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{}, "PG_NIX_LOG_DISABLE=1")
		if code := r.run("--check"); code != 1 || !strings.Contains(r.stdout.String(), "disabled") {
			t.Errorf("code=%d out=%s", code, r.stdout)
		}
	})
	t.Run("unwritable dir", func(t *testing.T) {
		r := newRig(t, fakecmd.Spec{})
		if err := os.WriteFile(r.state, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := r.run("--check"); code != 1 || !strings.Contains(r.stdout.String(), "writable: no") {
			t.Errorf("code=%d out=%s", code, r.stdout)
		}
	})
}

func TestDeadCollectorNeverChangesExitCodeOrStderrAndIsBounded(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close() // nothing listens: connection refused
	r := newRig(t, fakecmd.Spec{Fixture: golden("local-build.jsonl"), Exit: 7, Stderr: "child-says-hi\n"})
	r.d.NewTelemetry = DefaultDeps("x").NewTelemetry
	r.d.FlushTimeout = 400 * time.Millisecond
	started := time.Now()
	code := r.run("--otlp-endpoint", "http://"+addr, "fake-nix")
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if r.stderr.Len() != 0 || r.childErr.String() != "child-says-hi\n" {
		t.Errorf("wrapper stderr=%q child stderr=%q", r.stderr, r.childErr)
	}
	if d := time.Since(started); d > 10*time.Second {
		t.Errorf("a dead collector blocked the command for %v", d)
	}
}

func TestSpanQueueIsFarAboveSDKDefault(t *testing.T) {
	if SpanQueueSize < 8*otelsetup.DefaultSpanQueueSize {
		t.Errorf("SpanQueueSize %d is not well above the SDK default %d", SpanQueueSize, otelsetup.DefaultSpanQueueSize)
	}
}

func TestExitCodeMapping(t *testing.T) {
	if got := exitCode(nil, errors.New("x")); got != 126 {
		t.Errorf("nil state = %d", got)
	}
}
