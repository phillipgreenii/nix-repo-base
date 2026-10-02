package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type harness struct {
	rec    *Recorder
	spans  *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
	parent trace.Span
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	ctx, parent := tp.Tracer("t").Start(context.Background(), "nix.invocation")
	r, err := New(ctx, tp.Tracer("t"), mp.Meter("m"))
	if err != nil {
		t.Fatal(err)
	}
	return &harness{rec: r, spans: sr, reader: reader, parent: parent}
}

func (h *harness) ended() []sdktrace.ReadOnlySpan { return h.spans.Ended() }

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func (h *harness) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func counter(t *testing.T, rm metricdata.ResourceMetrics, outcome string) int64 {
	t.Helper()
	m := findMetric(rm, MetricDerivations)
	if m == nil {
		return 0
	}
	sum := m.Data.(metricdata.Sum[int64])
	for _, dp := range sum.DataPoints {
		if v, ok := dp.Attributes.Value("outcome"); ok && v.AsString() == outcome {
			return dp.Value
		}
	}
	return 0
}

var (
	start = time.Unix(1_800_000_000, 0)
	end   = start.Add(3500 * time.Millisecond)
)

func TestBuildSpanAndMetrics(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "thing-1.0", Path: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-thing-1.0.drv",
		Start: start, End: end,
	})
	spans := h.ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	s := spans[0]
	if s.Name() != "nix.build" || !s.StartTime().Equal(start) || !s.EndTime().Equal(end) {
		t.Errorf("span = %s %v..%v", s.Name(), s.StartTime(), s.EndTime())
	}
	if s.Parent().SpanID() != h.parent.SpanContext().SpanID() {
		t.Error("build span is not a child of the invocation span")
	}
	if a := attrs(s); a["nix.drv.name"].AsString() != "thing-1.0" {
		t.Errorf("attrs = %v", a)
	}
	if s.Status().Code == codes.Error {
		t.Errorf("status = %v", s.Status())
	}
	rm := h.collect(t)
	if counter(t, rm, OutcomeBuilt) != 1 {
		t.Error("built not counted")
	}
	hist := findMetric(rm, MetricBuildDuration).Data.(metricdata.Histogram[float64])
	dp := hist.DataPoints[0]
	if dp.Sum != 3.5 || dp.Count != 1 {
		t.Errorf("duration sum=%v count=%d, want 3.5s", dp.Sum, dp.Count)
	}
	if v, _ := dp.Attributes.Value("status"); v.AsString() != "ok" {
		t.Errorf("status label = %v", v)
	}
	// The derivation name must never be a metric label (cardinality).
	for _, kv := range dp.Attributes.ToSlice() {
		if kv.Key != "status" {
			t.Errorf("unexpected metric label %s", kv.Key)
		}
	}
}

func TestFailedBuildIsErrorSpanAndFailedOutcome(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "bad", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrBuildFailed, Reason: "builder failed with exit code 3",
	})
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "dep", Start: end, End: end, Instant: true,
		Failed: true, ErrorType: nixlog.ErrDependencyFailed, Reason: "1 dependency failed",
	})
	spans := h.ended()
	if len(spans) != 2 {
		t.Fatalf("spans = %d", len(spans))
	}
	for i, wantType := range []string{"build_failed", "dependency_failed"} {
		s := spans[i]
		if s.Status().Code != codes.Error || s.Status().Description == "" {
			t.Errorf("span %d status = %v", i, s.Status())
		}
		if attrs(s)["error.type"].AsString() != wantType {
			t.Errorf("span %d error.type = %v", i, attrs(s))
		}
	}
	rm := h.collect(t)
	if counter(t, rm, OutcomeFailed) != 2 || counter(t, rm, OutcomeBuilt) != 0 {
		t.Errorf("outcomes: failed=%d built=%d", counter(t, rm, OutcomeFailed), counter(t, rm, OutcomeBuilt))
	}
	// Only the started build has a duration; the instant one has none.
	hist := findMetric(rm, MetricBuildDuration).Data.(metricdata.Histogram[float64])
	if hist.DataPoints[0].Count != 1 {
		t.Errorf("duration count = %d, want 1", hist.DataPoints[0].Count)
	}
}

func TestAbortedActivityGetsErrorSpanButNoOutcome(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "killed", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrAborted, Reason: "nix exited",
	})
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindSubstitute, Name: "p", Server: "c", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrAborted, Reason: "nix exited",
	})
	if spans := h.ended(); len(spans) != 2 || spans[0].Status().Code != codes.Error || spans[1].Status().Code != codes.Error {
		t.Fatalf("spans = %v", spans)
	}
	rm := h.collect(t)
	if m := findMetric(rm, MetricDerivations); m != nil {
		t.Errorf("an aborted activity must not be counted as an outcome: %+v", m)
	}
}

func TestSubstituteSpanAttrsAndNoSpanStillCounts(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindSubstitute, Name: "lib", Path: "/nix/store/x-lib",
		Server: "cache.nixos.org", Start: start, End: end,
	})
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindSubstitute, Name: "tiny", Server: "cache.nixos.org",
		Start: start, End: start.Add(time.Millisecond), NoSpan: true,
	})
	spans := h.ended()
	if len(spans) != 1 || spans[0].Name() != "nix.substitute" {
		t.Fatalf("spans = %v", spans)
	}
	if attrs(spans[0])["server.address"].AsString() != "cache.nixos.org" {
		t.Errorf("attrs = %v", attrs(spans[0]))
	}
	if got := counter(t, h.collect(t), OutcomeSubstituted); got != 2 {
		t.Errorf("substituted = %d, want 2 (the short one is counted without a span)", got)
	}
}

func TestWaitSpanHasNoMetric(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{Kind: nixlog.KindWait, Name: "locked", Start: start, End: end})
	if spans := h.ended(); len(spans) != 1 || spans[0].Name() != "nix.build.wait" {
		t.Fatalf("spans = %v", spans)
	}
	if m := findMetric(h.collect(t), MetricDerivations); m != nil {
		t.Errorf("wait produced a derivation metric: %+v", m)
	}
}

func TestInvocationDuration(t *testing.T) {
	h := newHarness(t)
	h.rec.Invocation("nix", 2*time.Second, false)
	h.rec.Invocation("nix", 4*time.Second, true)
	hist := findMetric(h.collect(t), MetricInvocationLength).Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 2 {
		t.Fatalf("data points = %d", len(hist.DataPoints))
	}
	byStatus := map[string]float64{}
	for _, dp := range hist.DataPoints {
		s, _ := dp.Attributes.Value("status")
		c, _ := dp.Attributes.Value("command")
		if c.AsString() != "nix" {
			t.Errorf("command = %v", c)
		}
		byStatus[s.AsString()] = dp.Sum
	}
	if byStatus["ok"] != 2 || byStatus["error"] != 4 {
		t.Errorf("by status = %v", byStatus)
	}
}

func TestMetricNamesAreInTheSharedCatalog(t *testing.T) {
	catalog := map[string]otelsetup.MetricSpec{}
	for _, m := range otelsetup.Metrics {
		catalog[m.Name] = m
	}
	for _, name := range []string{MetricDerivations, MetricBuildDuration, MetricInvocationLength} {
		spec, ok := catalog[name]
		if !ok {
			t.Errorf("%s is not in otelsetup.Metrics: dashboards lint against that catalog", name)
			continue
		}
		h := newHarness(t)
		h.rec.Emit(nixlog.Activity{Kind: nixlog.KindBuild, Name: "n", Start: start, End: end})
		h.rec.Invocation("nix", time.Second, false)
		m := findMetric(h.collect(t), name)
		if m == nil || m.Unit != spec.Unit {
			t.Errorf("%s unit = %v, catalog says %q", name, m, spec.Unit)
		}
	}
}
