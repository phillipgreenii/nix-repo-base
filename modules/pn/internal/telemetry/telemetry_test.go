package telemetry_test

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
)

func attrOf(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestSpanHierarchyAndAttributes(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)

	ctx, verb := tel.StartVerb(ctx, "workspace status")
	rctx, endRepo := telemetry.StartRepo(ctx, "repo-a")
	_, endExec := telemetry.StartExec(rctx, "git")
	endExec(0, nil)
	endRepo(nil)
	verb.End(0, nil)

	vs, rs, es := rec.ByName("pn.verb"), rec.ByName("pn.repo"), rec.ByName("pn.exec")
	if len(vs) != 1 || len(rs) != 1 || len(es) != 1 {
		t.Fatalf("span counts verb/repo/exec = %d/%d/%d, want 1/1/1", len(vs), len(rs), len(es))
	}
	if rs[0].Parent().SpanID() != vs[0].SpanContext().SpanID() {
		t.Error("pn.repo is not a child of pn.verb")
	}
	if es[0].Parent().SpanID() != rs[0].SpanContext().SpanID() {
		t.Error("pn.exec is not a child of pn.repo")
	}
	if rs[0].Parent().TraceID() != vs[0].SpanContext().TraceID() {
		t.Error("pn.repo is in a different trace")
	}
	if v, ok := attrOf(rs[0], "pn.repo"); !ok || v.AsString() != "repo-a" {
		t.Errorf("pn.repo attr = %v", v)
	}
	if v, ok := attrOf(rs[0], "pn.verb"); !ok || v.AsString() != "workspace status" {
		t.Errorf("pn.repo verb attr = %v", v)
	}
	if v, ok := attrOf(es[0], "process.executable.name"); !ok || v.AsString() != "git" {
		t.Errorf("pn.exec executable attr = %v", v)
	}
	if v, ok := attrOf(es[0], "process.exit.code"); !ok || v.AsInt64() != 0 {
		t.Errorf("pn.exec exit code attr = %v", v)
	}
	for _, s := range []sdktrace.ReadOnlySpan{vs[0], rs[0], es[0]} {
		if s.SpanKind().String() != "internal" {
			t.Errorf("%s kind = %v, want internal", s.Name(), s.SpanKind())
		}
		if s.Status().Code == codes.Error {
			t.Errorf("%s unexpectedly Error", s.Name())
		}
	}
}

func TestFailureMarksSpansError(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, "workspace build")
	_, endExec := telemetry.StartExec(ctx, "nix")
	endExec(2, errors.New("nix exited 2"))
	verb.End(2, errors.New("build failed"))

	for _, name := range []string{"pn.exec", "pn.verb"} {
		ss := rec.ByName(name)
		if len(ss) != 1 {
			t.Fatalf("%s spans = %d", name, len(ss))
		}
		if ss[0].Status().Code != codes.Error {
			t.Errorf("%s status = %v, want Error", name, ss[0].Status().Code)
		}
		if _, ok := attrOf(ss[0], "error.type"); !ok {
			t.Errorf("%s missing error.type", name)
		}
		if v, ok := attrOf(ss[0], "process.exit.code"); !ok || v.AsInt64() != 2 {
			t.Errorf("%s exit code = %v", name, v)
		}
	}
}

func TestVerbEndRecordsDurationMetricOnce(t *testing.T) {
	tel, rec := telemetrytest.New()
	_, verb := tel.StartVerb(context.Background(), "workspace status")
	verb.End(0, nil)
	verb.End(0, nil) // idempotent

	rm := rec.Metrics(t)
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "pn.command.duration" {
				continue
			}
			found = true
			if m.Unit != "s" {
				t.Errorf("unit = %q, want s", m.Unit)
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("data type %T, want Histogram[float64]", m.Data)
			}
			if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
				t.Fatalf("datapoints = %+v, want exactly one with count 1", h.DataPoints)
			}
			dp := h.DataPoints[0]
			if v, _ := dp.Attributes.Value("pn.verb"); v.AsString() != "workspace status" {
				t.Errorf("verb dim = %q", v.AsString())
			}
			if v, _ := dp.Attributes.Value("status"); v.AsString() != "ok" {
				t.Errorf("status dim = %q", v.AsString())
			}
			if _, has := dp.Attributes.Value("pn.repo"); has {
				t.Error("repo MUST NOT be a dimension of the per-command histogram")
			}
		}
	}
	if !found {
		t.Fatal("pn.command.duration not recorded")
	}
}

func TestFailedVerbRecordsErrorStatus(t *testing.T) {
	tel, rec := telemetrytest.New()
	_, verb := tel.StartVerb(context.Background(), "workspace push")
	verb.End(1, errors.New("boom"))
	rm := rec.Metrics(t)
	h := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
	if v, _ := h.DataPoints[0].Attributes.Value("status"); v.AsString() != "error" {
		t.Errorf("status dim = %q, want error", v.AsString())
	}
}

func TestNilVerbSpanEndIsNoop(t *testing.T) {
	var v *telemetry.VerbSpan
	v.End(1, errors.New("x")) // MUST NOT panic
}

func TestNoTelemetryInContextIsNoop(t *testing.T) {
	ctx, endRepo := telemetry.StartRepo(context.Background(), "r")
	_, endExec := telemetry.StartExec(ctx, "git")
	endExec(1, errors.New("x"))
	endRepo(errors.New("y"))
}

func TestShutdownRunsOnceAndReturnsFirstResult(t *testing.T) {
	tel, rec := telemetrytest.New()
	rec.ShutdownErr = errors.New("flush failed")
	err1 := tel.Shutdown(context.Background())
	err2 := tel.Shutdown(context.Background())
	if err1 == nil || err2 == nil || err1.Error() != err2.Error() {
		t.Errorf("shutdown results = %v / %v, want same non-nil error", err1, err2)
	}
	if rec.ShutdownCount() != 1 || tel.ShutdownCalls() != 1 {
		t.Errorf("shutdown ran %d (hook) / %d (guard) times, want 1", rec.ShutdownCount(), tel.ShutdownCalls())
	}
}

func TestShutdownNilSafe(t *testing.T) {
	var tel *telemetry.Telemetry
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Shutdown = %v", err)
	}
}

// The no-op path MUST make zero network connections, leave no goroutines
// behind and create no file under HOME.
func TestNoopPath_NoConnectionsNoGoroutinesNoFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	var conns atomic.Int32
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			_ = c.Close()
		}
	}()
	// Even with the listener's address present in the environment, an unset
	// endpoint MUST resolve to no-op. (A decoy: nothing may be dialed.)
	t.Setenv("PN_TEST_DECOY_ENDPOINT", lis.Addr().String())

	before := runtime.NumGoroutine()
	// telemetrycfg resolves no endpoint -> New gets "".
	tel, err := telemetry.New(context.Background(), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if tel.Enabled() {
		t.Fatal("telemetry enabled with empty endpoint")
	}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, "workspace status")
	rctx, endRepo := telemetry.StartRepo(ctx, "r")
	_, endExec := telemetry.StartExec(rctx, "git")
	endExec(0, nil)
	endRepo(nil)
	verb.End(0, nil)
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond) // let any (forbidden) dial land
	if n := conns.Load(); n != 0 {
		t.Errorf("no-op path made %d connections, want 0", n)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d -> %d, want no growth", before, after)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("no-op path created files under HOME: %v", entries)
	}
}

// With an endpoint, a verb's spans and the duration metric reach the collector.
func TestEnabled_ExportsToCollector(t *testing.T) {
	var traces, metrics atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/traces":
			traces.Add(1)
		case "/v1/metrics":
			metrics.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tel, err := telemetry.New(context.Background(), "test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !tel.Enabled() {
		t.Fatal("telemetry not enabled")
	}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, "workspace status")
	_, endRepo := telemetry.StartRepo(ctx, "r")
	endRepo(nil)
	verb.End(0, nil)
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if traces.Load() == 0 {
		t.Error("collector received no /v1/traces request")
	}
	if metrics.Load() == 0 {
		t.Error("collector received no /v1/metrics request")
	}
}

// A collector that is down MUST NOT hang pn: the flush is bounded.
func TestCollectorDown_ShutdownBounded(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + lis.Addr().String()
	_ = lis.Close() // nothing listens: connection refused

	tel, err := telemetry.New(context.Background(), "test", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	_, verb := tel.StartVerb(ctx, "workspace status")
	verb.End(0, nil)

	start := time.Now()
	_ = tel.Shutdown(context.Background())
	if d := time.Since(start); d > telemetry.ShutdownTimeout+2*time.Second {
		t.Errorf("Shutdown took %v with the collector down, want <= ~%v", d, telemetry.ShutdownTimeout)
	}
}

// A collector that accepts but never answers is also bounded.
func TestCollectorHung_ShutdownBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	tel, err := telemetry.New(context.Background(), "test", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	_, verb := tel.StartVerb(ctx, "workspace status")
	verb.End(0, nil)

	start := time.Now()
	_ = tel.Shutdown(context.Background())
	if d := time.Since(start); d > telemetry.ShutdownTimeout+2*time.Second {
		t.Errorf("Shutdown took %v with a hung collector, want <= ~%v", d, telemetry.ShutdownTimeout)
	}
}

// pn.plan.builds carries the catalog's boundaries (400 an exact edge), is
// labelled by verb only and works on the no-op Telemetry.
func TestRecordPlan_HistogramAndSpanAttributes(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, end := telemetry.StartRepo(ctx, "terminal")
	telemetry.RecordPlan(ctx, "apply", telemetry.Plan{Builds: 423, Fetches: 5, DownloadMiB: 2.5, HasDownload: true})
	end(nil)

	spans := rec.ByName(telemetry.SpanRepo)
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	got := map[string]any{}
	for _, a := range spans[0].Attributes() {
		got[string(a.Key)] = a.Value.AsInterface()
	}
	if got["pn.plan.build"] != int64(423) || got["pn.plan.fetch"] != int64(5) || got["pn.plan.download_mib"] != 2.5 {
		t.Errorf("attributes = %v", got)
	}

	var h metricdata.Histogram[int64]
	for _, sm := range rec.Metrics(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == telemetry.MetricPlanBuilds {
				h = m.Data.(metricdata.Histogram[int64])
			}
		}
	}
	if len(h.DataPoints) != 1 || h.DataPoints[0].Sum != 423 || h.DataPoints[0].Attributes.Len() != 1 {
		t.Fatalf("histogram = %+v", h.DataPoints)
	}
	if !slices.Contains(h.DataPoints[0].Bounds, 400.0) {
		t.Errorf("bounds %v lack 400", h.DataPoints[0].Bounds)
	}

	// No-op Telemetry and a context without a span must not panic.
	telemetry.RecordPlan(context.Background(), "build", telemetry.Plan{})
}

// Every metric pn creates MUST be in the shared catalog (the dashboard lint
// checks PromQL names against it), and pn.plan.builds carries its boundaries
// there.
func TestMetricNamesAreInTheSharedCatalog(t *testing.T) {
	for _, name := range []string{telemetry.MetricCommandDuration, telemetry.MetricPlanBuilds} {
		i := slices.IndexFunc(otelsetup.Metrics, func(m otelsetup.MetricSpec) bool { return m.Name == name })
		if i < 0 {
			t.Errorf("%s is not in otelsetup.Metrics", name)
		}
	}
	for _, m := range otelsetup.Metrics {
		if m.Name == telemetry.MetricPlanBuilds && !slices.Contains(m.Buckets, 400.0) {
			t.Errorf("catalog buckets for %s = %v, want an exact 400 edge", m.Name, m.Buckets)
		}
	}
}

// pn.plan.builds MUST use the same pn.verb values as pn.command.duration
// ("workspace build" / "workspace apply"): no bare build/apply series may sit
// beside them.
func TestRecordPlan_VerbLabelIsFullWorkspaceVerb(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	for _, verb := range []string{"build", "apply", "workspace build", "workspace apply"} {
		telemetry.RecordPlan(ctx, verb, telemetry.Plan{Builds: 1})
	}
	got := map[string]bool{}
	for _, sm := range rec.Metrics(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != telemetry.MetricPlanBuilds {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[int64]).DataPoints {
				v, _ := dp.Attributes.Value("pn.verb")
				got[v.AsString()] = true
			}
		}
	}
	want := map[string]bool{"workspace build": true, "workspace apply": true}
	if !maps.Equal(got, want) {
		t.Errorf("pn.verb values = %v, want exactly %v", got, want)
	}
}
