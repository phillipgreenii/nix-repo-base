package recorder

import (
	"context"
	"fmt"
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
	h.rec.Invocation("nix", "flake check", 2*time.Second, false, "")
	h.rec.Invocation("nix", "build", 4*time.Second, true, "build_failed")
	hist := findMetric(h.collect(t), MetricInvocationLength).Data.(metricdata.Histogram[float64])
	if len(hist.DataPoints) != 2 {
		t.Fatalf("data points = %d", len(hist.DataPoints))
	}
	byStatus := map[string]float64{}
	bySub := map[string]string{}
	for _, dp := range hist.DataPoints {
		s, _ := dp.Attributes.Value("status")
		c, _ := dp.Attributes.Value("command")
		if c.AsString() != "nix" {
			t.Errorf("command = %v", c)
		}
		sub, _ := dp.Attributes.Value("subcommand")
		bySub[s.AsString()] = sub.AsString()
		byStatus[s.AsString()] = dp.Sum
	}
	if bySub["ok"] != "flake check" || bySub["error"] != "build" {
		t.Errorf("subcommand label by status = %v", bySub)
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
		h.rec.Invocation("nix", "build", time.Second, false, "")
		m := findMetric(h.collect(t), name)
		if m == nil || m.Unit != spec.Unit {
			t.Errorf("%s unit = %v, catalog says %q", name, m, spec.Unit)
		}
	}
}

func TestSubstituteBytesIsASpanAttributeOnlyWhenSeen(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{Kind: nixlog.KindSubstitute, Name: "a", Server: "c.example", Start: start, End: end, Bytes: 4096, HasBytes: true})
	h.rec.Emit(nixlog.Activity{Kind: nixlog.KindSubstitute, Name: "b", Server: "c.example", Start: start, End: end})
	h.rec.Emit(nixlog.Activity{Kind: nixlog.KindBuild, Name: "c", Start: start, End: end, Bytes: 7, HasBytes: true})
	spans := h.ended()
	if len(spans) != 3 {
		t.Fatalf("spans = %d", len(spans))
	}
	if v, ok := attrs(spans[0])["nix.substitute.bytes"]; !ok || v.AsInt64() != 4096 {
		t.Errorf("bytes = %v (found %v)", v, ok)
	}
	for _, s := range spans[1:] {
		if _, ok := attrs(s)["nix.substitute.bytes"]; ok {
			t.Errorf("%s span must not carry nix.substitute.bytes", s.Name())
		}
	}
	// Not a metric label: derivations carries only outcome.
	m := findMetric(h.collect(t), MetricDerivations).Data.(metricdata.Sum[int64])
	for _, dp := range m.DataPoints {
		for _, kv := range dp.Attributes.ToSlice() {
			if kv.Key != "outcome" {
				t.Errorf("unexpected metric label %s", kv.Key)
			}
		}
	}
}

func TestInvocationMetricCarriesOnlyCommandSubcommandAndStatus(t *testing.T) {
	h := newHarness(t)
	h.rec.Invocation("nix", "build", time.Second, false, "")
	hist := findMetric(h.collect(t), MetricInvocationLength).Data.(metricdata.Histogram[float64])
	for _, kv := range hist.DataPoints[0].Attributes.ToSlice() {
		if kv.Key != "command" && kv.Key != "subcommand" && kv.Key != "status" {
			t.Errorf("unexpected invocation metric label %s", kv.Key)
		}
	}
}

func invocationLabels(t *testing.T, h *harness) []map[string]string {
	t.Helper()
	hist := findMetric(h.collect(t), MetricInvocationLength).Data.(metricdata.Histogram[float64])
	var out []map[string]string
	for _, dp := range hist.DataPoints {
		m := map[string]string{}
		for _, kv := range dp.Attributes.ToSlice() {
			m[string(kv.Key)] = kv.Value.AsString()
		}
		out = append(out, m)
	}
	return out
}

// failure_kind labels a failed run only, and the label is the closed set: a
// value outside nixlog.FailureKinds (including "") is recorded as "other".
func TestFailureKindLabelIsBoundedAndOnlyOnFailedRuns(t *testing.T) {
	h := newHarness(t)
	h.rec.Invocation("nix", "build", time.Second, false, nixlog.ErrBuildFailed) // ignored: ok run
	for _, k := range nixlog.FailureKinds {
		h.rec.Invocation("nix", "build", time.Second, true, k)
	}
	h.rec.Invocation("nix", "build", time.Second, true, "")
	h.rec.Invocation("nix", "build", time.Second, true, "some free text: /nix/store/x")
	seen := map[string]bool{}
	for _, l := range invocationLabels(t, h) {
		if l["status"] == "ok" {
			if _, has := l["failure_kind"]; has {
				t.Errorf("an ok run carries failure_kind: %v", l)
			}
			continue
		}
		k, has := l["failure_kind"]
		if !has {
			t.Fatalf("a failed run lacks failure_kind: %v", l)
		}
		seen[k] = true
	}
	for k := range seen {
		if BoundedFailureKind(k) != k {
			t.Errorf("failure_kind %q is outside the closed set", k)
		}
	}
	if len(seen) != len(nixlog.FailureKinds) {
		t.Errorf("seen kinds = %v, want exactly %v", seen, nixlog.FailureKinds)
	}
}

// The closed set is exactly the documented one: adding a kind is a deliberate,
// reviewed change to this list (and to README, SKILL.md and ADR 0030).
func TestFailureKindsAreTheDocumentedClosedSet(t *testing.T) {
	want := []string{"build_failed", "dependency_failed", "hash_mismatch", "eval_error", "signal", "other"}
	if fmt.Sprint(nixlog.FailureKinds) != fmt.Sprint(want) {
		t.Errorf("FailureKinds = %v, want %v", nixlog.FailureKinds, want)
	}
}

// nix.error.message and nix.failed.drvs are free text: they never become
// metric labels, whatever the run carried.
func TestFreeTextIsNeverAMetricLabel(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "n", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrHashMismatch, Reason: "free text", Placeholder: true,
	})
	h.rec.Invocation("nix", "build", time.Second, true, nixlog.ErrHashMismatch)
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			var sets []attribute.Set
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					sets = append(sets, dp.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					sets = append(sets, dp.Attributes)
				}
			}
			for _, set := range sets {
				for _, kv := range set.ToSlice() {
					switch string(kv.Key) {
					case "nix.error.message", "nix.failed.drvs", "nix.drv.name", "nix.hash_mismatch.placeholder":
						t.Errorf("%s carries span-only attribute %s as a label", m.Name, kv.Key)
					}
				}
			}
		}
	}
}

func TestHashMismatchSpanCarriesPlaceholderAttrOnlyWhenPlaceholder(t *testing.T) {
	h := newHarness(t)
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "a", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrHashMismatch, Placeholder: true,
	})
	h.rec.Emit(nixlog.Activity{
		Kind: nixlog.KindBuild, Name: "b", Start: start, End: end,
		Failed: true, ErrorType: nixlog.ErrHashMismatch,
	})
	spans := h.ended()
	if v, ok := attrs(spans[0])["nix.hash_mismatch.placeholder"]; !ok || !v.AsBool() {
		t.Errorf("placeholder attr = %v (found %v)", v, ok)
	}
	if v, ok := attrs(spans[0])["error.type"]; !ok || v.AsString() != "hash_mismatch" {
		t.Errorf("error.type = %v", v)
	}
	if _, ok := attrs(spans[1])["nix.hash_mismatch.placeholder"]; ok {
		t.Error("a real (non-placeholder) mismatch must not carry the attribute")
	}
	// Decision: a placeholder mismatch still counts as a failed derivation;
	// dashboards filter on the span attribute.
	if got := counter(t, h.collect(t), OutcomeFailed); got != 2 {
		t.Errorf("failed derivations = %d, want 2 (placeholder mismatches count)", got)
	}
}
