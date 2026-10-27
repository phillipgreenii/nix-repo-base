// Package telemetry is pn's OpenTelemetry facade (ADR 0028).
//
// It wraps the shared phillipgreenii/x/otelsetup package with pn's own span
// vocabulary:
//
//	pn.verb  (root, one per pn invocation)
//	  pn.repo  (one per repo a workspace verb touches)
//	    pn.exec  (one per subprocess, emitted by the exec.Runner decorator)
//
// and the pn.command.duration histogram (seconds), recorded once per
// invocation when the root span ends.
//
// Design notes:
//   - Null Object: with no OTLP endpoint resolved, New returns a working
//     Telemetry backed by no-op providers. No exporter is built, no network
//     call is made, no goroutine is started and no file is created, so every
//     call site can use the API unconditionally.
//   - The Telemetry travels in the context.Context (WithTelemetry / From);
//     library code that has no Telemetry in its context still works (no-op).
//   - Shutdown is idempotent and bounded: a flush failure is returned to the
//     caller but MUST NOT change pn's exit code (cli.Execute ignores it).
//   - WHETHER and WHERE to export is decided elsewhere, by telemetrycfg
//     (flag > env > telemetry.toml, plus the force-off controls); this package
//     only turns the resolved endpoint into exporters.
package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// ServiceName is the service.name resource attribute. It is set in code,
	// never taken from OTEL_SERVICE_NAME.
	ServiceName = "pn"

	// ShutdownTimeout bounds the final flush so a down collector can never
	// hang pn on exit.
	ShutdownTimeout = 3 * time.Second

	// Span and metric names. Metric names MUST exist in otelsetup's
	// generated metrics.json catalog.
	SpanVerb              = "pn.verb"
	SpanRepo              = "pn.repo"
	SpanExec              = "pn.exec"
	MetricCommandDuration = "pn.command.duration"
	MetricPlanBuilds      = "pn.plan.builds"
)

// Attribute keys.
const (
	AttrVerb       = "pn.verb"
	AttrRepo       = "pn.repo"
	AttrStatus     = "status"
	AttrExitCode   = "process.exit.code"
	AttrErrorType  = "error.type"
	AttrExecutable = "process.executable.name"
	// AttrToolNotFound marks a pn.exec span that records a refused launch: the
	// command was on neither PATH nor the system profile, so no process ran.
	AttrToolNotFound = "pn.exec.tool_not_found"
	StatusOK         = "ok"
	StatusError      = "error"

	// Build-plan attributes on the pn.repo span (bead pg2-sdbhh4.10); names are
	// aligned with the wrapper's nix.plan.build / nix.plan.fetch.
	AttrPlanBuild       = "pn.plan.build"
	AttrPlanFetch       = "pn.plan.fetch"
	AttrPlanDownloadMiB = "pn.plan.download_mib"
)

// Telemetry is the process-wide telemetry handle.
type Telemetry struct {
	tracer          trace.Tracer
	commandDuration metric.Float64Histogram
	planBuilds      metric.Int64Histogram
	enabled         bool

	shutdown func(context.Context) error
	timeout  time.Duration

	once    sync.Once
	downErr error
	calls   int // number of Shutdown bodies actually executed (test seam)
	mu      sync.Mutex
}

// New builds telemetry for endpoint (telemetrycfg.Resolution.Endpoint). An empty endpoint yields the no-op
// Telemetry. On an Init error the no-op Telemetry is returned together with
// the error so callers may log it and carry on (telemetry must never stop pn).
func New(ctx context.Context, version, endpoint string, opts ...otelsetup.Option) (*Telemetry, error) {
	opts = append([]otelsetup.Option{otelsetup.WithTimeout(ShutdownTimeout)}, opts...)
	ot, err := otelsetup.Init(ctx, otelsetup.Config{
		Endpoint:    endpoint,
		ServiceName: ServiceName,
		Version:     version,
	}, opts...)
	if err != nil {
		return Disabled(), err
	}
	t := &Telemetry{enabled: ot.Enabled(), timeout: ShutdownTimeout}
	t.tracer = ot.Tracer
	t.commandDuration = mustHistogram(ot.Meter)
	t.planBuilds = mustPlanBuilds(ot.Meter)
	t.shutdown = ot.Shutdown
	return t, nil
}

// Disabled returns the no-op Telemetry.
func Disabled() *Telemetry {
	return &Telemetry{
		tracer:          tracenoop.NewTracerProvider().Tracer(ServiceName),
		commandDuration: mustHistogram(metricnoop.NewMeterProvider().Meter(ServiceName)),
		planBuilds:      mustPlanBuilds(metricnoop.NewMeterProvider().Meter(ServiceName)),
	}
}

// NewWithProviders builds an enabled Telemetry from caller-supplied providers
// (in-memory exporters in tests). shutdown may be nil.
func NewWithProviders(tracer trace.Tracer, meter metric.Meter, shutdown func(context.Context) error) *Telemetry {
	return &Telemetry{
		tracer:          tracer,
		commandDuration: mustHistogram(meter),
		planBuilds:      mustPlanBuilds(meter),
		enabled:         true,
		shutdown:        shutdown,
		timeout:         ShutdownTimeout,
	}
}

func mustHistogram(m metric.Meter) metric.Float64Histogram {
	h, err := m.Float64Histogram(
		MetricCommandDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Wall-clock duration of one pn invocation, by verb and status."),
	)
	if err != nil {
		// Only possible for an invalid instrument name; ours is constant.
		h, _ = metricnoop.NewMeterProvider().Meter(ServiceName).Float64Histogram(MetricCommandDuration)
	}
	return h
}

// mustPlanBuilds creates the pn.plan.builds histogram. Its explicit boundaries
// come from the shared catalog (otelsetup.PlanBuildsBuckets), so 400 is an exact
// edge; as an advisory they also apply to a meter provider with no otelsetup
// View (in-memory test readers), while otelsetup's own View applies the same
// catalog entry in production.
func mustPlanBuilds(m metric.Meter) metric.Int64Histogram {
	h, err := m.Int64Histogram(
		MetricPlanBuilds,
		metric.WithUnit("{derivation}"),
		metric.WithDescription("Derivations in the nix build plan of one pn build/apply run, by verb."),
		metric.WithExplicitBucketBoundaries(otelsetup.PlanBuildsBuckets...),
	)
	if err != nil {
		h, _ = metricnoop.NewMeterProvider().Meter(ServiceName).Int64Histogram(MetricPlanBuilds)
	}
	return h
}

// Plan is the size of the nix build plan one build/apply run printed.
type Plan struct {
	// Builds and Fetches are the derivations to build and paths to fetch (0 when
	// nix printed no such plan).
	Builds  int
	Fetches int
	// DownloadMiB is the fetch download size, valid when HasDownload.
	DownloadMiB float64
	HasDownload bool
}

// RecordPlan records one run's plan: pn.plan.build / pn.plan.fetch (and
// pn.plan.download_mib when the fetch message carried a size) on the span in ctx
// (the pn.repo span), and ONE pn.plan.builds observation labelled with verb
// (build or apply) only. Call it once per run that reached the build step.
func RecordPlan(ctx context.Context, verb string, p Plan) {
	attrs := []attribute.KeyValue{
		attribute.Int(AttrPlanBuild, p.Builds),
		attribute.Int(AttrPlanFetch, p.Fetches),
	}
	if p.HasDownload {
		attrs = append(attrs, attribute.Float64(AttrPlanDownloadMiB, p.DownloadMiB))
	}
	trace.SpanFromContext(ctx).SetAttributes(attrs...)
	From(ctx).planBuilds.Record(context.WithoutCancel(ctx), int64(p.Builds),
		metric.WithAttributes(attribute.String(AttrVerb, verb)))
}

// Enabled reports whether telemetry is exporting.
func (t *Telemetry) Enabled() bool { return t != nil && t.enabled }

// Shutdown flushes and releases exporters exactly once, bounded by both ctx
// and ShutdownTimeout. Later calls return the first call's result. Safe on a
// nil or disabled Telemetry.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.once.Do(func() {
		t.mu.Lock()
		t.calls++
		t.mu.Unlock()
		if t.shutdown == nil {
			return
		}
		timeout := t.timeout
		if timeout <= 0 {
			timeout = ShutdownTimeout
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		t.downErr = t.shutdown(cctx)
	})
	return t.downErr
}

// ShutdownCalls reports how many times the shutdown body actually ran.
func (t *Telemetry) ShutdownCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type ctxKey int

const (
	telemetryKey ctxKey = iota
	verbKey
)

// WithTelemetry stores t in ctx.
func WithTelemetry(ctx context.Context, t *Telemetry) context.Context {
	return context.WithValue(ctx, telemetryKey, t)
}

// From returns the Telemetry stored in ctx, or the no-op Telemetry.
func From(ctx context.Context) *Telemetry {
	if t, ok := ctx.Value(telemetryKey).(*Telemetry); ok && t != nil {
		return t
	}
	return noop
}

var noop = Disabled()

// VerbSpan is the root pn.verb span plus the data needed to record
// pn.command.duration when it ends.
type VerbSpan struct {
	t     *Telemetry
	span  trace.Span
	verb  string
	start time.Time
	once  sync.Once
}

// StartVerb starts the root pn.verb span. The returned context carries the
// span (so pn.repo / pn.exec nest under it) and the verb name.
func (t *Telemetry) StartVerb(ctx context.Context, verb string) (context.Context, *VerbSpan) {
	ctx, span := t.tracer.Start(ctx, SpanVerb, trace.WithAttributes(attribute.String(AttrVerb, verb)))
	ctx = context.WithValue(ctx, verbKey, verb)
	return ctx, &VerbSpan{t: t, span: span, verb: verb, start: time.Now()}
}

// TraceID returns the root span's trace id as 32 lowercase hex characters, or
// "" for a nil VerbSpan or a no-op tracer (invalid span context). The caller
// feeds it to telemetrycfg.RunState.SetTraceID for the trace hint and the
// events.jsonl run_start/run_end records.
func (v *VerbSpan) TraceID() string {
	if v == nil {
		return ""
	}
	sc := v.span.SpanContext()
	if !sc.TraceID().IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// End finishes the root span exactly once: it records the exit code, marks
// the span Error (with error.type) when exitCode != 0, and records
// pn.command.duration with verb and status dimensions. A nil VerbSpan is a
// no-op (a command that never reached PersistentPreRunE, e.g. --help).
func (v *VerbSpan) End(exitCode int, err error) {
	if v == nil {
		return
	}
	v.once.Do(func() {
		status := StatusOK
		v.span.SetAttributes(attribute.Int(AttrExitCode, exitCode))
		if exitCode != 0 {
			status = StatusError
			setError(v.span, err)
		}
		v.span.End()
		v.t.commandDuration.Record(context.Background(), time.Since(v.start).Seconds(),
			metric.WithAttributes(
				attribute.String(AttrVerb, v.verb),
				attribute.String(AttrStatus, status),
			))
	})
}

// StartRepo starts a pn.repo span for repo, a child of the current span. The
// returned func ends it and MUST be called exactly once (pass the repo's
// error, or nil).
func StartRepo(ctx context.Context, repo string) (context.Context, func(error)) {
	verb, _ := ctx.Value(verbKey).(string)
	ctx, span := From(ctx).tracer.Start(ctx, SpanRepo, trace.WithAttributes(
		attribute.String(AttrRepo, repo),
		attribute.String(AttrVerb, verb),
	))
	return ctx, func(err error) {
		if err != nil {
			setError(span, err)
		}
		span.End()
	}
}

// StartExec starts a pn.exec span for a subprocess named name. The returned
// func ends it with the process exit code and error.
func StartExec(ctx context.Context, name string) (context.Context, func(exitCode int, err error)) {
	ctx, span := From(ctx).tracer.Start(ctx, SpanExec, trace.WithAttributes(
		attribute.String(AttrExecutable, name),
	))
	return ctx, func(exitCode int, err error) {
		if err != nil || exitCode != 0 {
			span.SetAttributes(attribute.Int(AttrExitCode, exitCode))
			setError(span, err)
		} else {
			span.SetAttributes(attribute.Int(AttrExitCode, 0))
		}
		span.End()
	}
}

// RecordToolNotFound emits a pn.exec span for a command pn refused to launch
// because it was found nowhere (exec.ResolveTool). No process ran, so the span
// is instantaneous; it carries the same process.executable.name and
// process.exit.code as a real pn.exec span, plus AttrToolNotFound=true, so the
// refusals are countable next to the exec spans they replace.
func RecordToolNotFound(ctx context.Context, name string, exitCode int, err error) {
	_, span := From(ctx).tracer.Start(ctx, SpanExec, trace.WithAttributes(
		attribute.String(AttrExecutable, name),
		attribute.Bool(AttrToolNotFound, true),
		attribute.Int(AttrExitCode, exitCode),
	))
	setError(span, err)
	span.End()
}

// setError marks span as failed with an error.type attribute.
func setError(span trace.Span, err error) {
	errType := "exit_status"
	desc := "non-zero exit"
	if err != nil {
		errType = fmt.Sprintf("%T", err)
		desc = err.Error()
		if len(desc) > 256 {
			desc = desc[:256]
		}
	}
	span.SetAttributes(attribute.String(AttrErrorType, errType))
	span.SetStatus(codes.Error, desc)
}
