// Package recorder is the Adapter from finished nix activities (package
// nixlog) to OpenTelemetry spans and metrics. Derivation names are span
// attributes only, never metric labels (cardinality).
package recorder

import (
	"context"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Metric names; they MUST match the otelsetup.Metrics catalog (a test checks).
const (
	MetricDerivations      = "nix.derivations"
	MetricBuildDuration    = "nix.build.duration"
	MetricInvocationLength = "nix.invocation.duration"
)

// Outcome values of the nix.derivations counter.
const (
	OutcomeBuilt       = "built"
	OutcomeSubstituted = "substituted"
	OutcomeFailed      = "failed"
)

// Recorder implements nixlog.Sink.
type Recorder struct {
	ctx           context.Context
	tracer        trace.Tracer
	derivations   metric.Int64Counter
	buildDuration metric.Float64Histogram
	invocation    metric.Float64Histogram
}

// New builds a Recorder whose spans are children of the span in ctx.
func New(ctx context.Context, tracer trace.Tracer, meter metric.Meter) (*Recorder, error) {
	d, err := meter.Int64Counter(MetricDerivations, metric.WithUnit("{derivation}"),
		metric.WithDescription("nix derivations built locally or substituted from a cache"))
	if err != nil {
		return nil, err
	}
	b, err := meter.Float64Histogram(MetricBuildDuration, metric.WithUnit("s"),
		metric.WithDescription("duration of local derivation builds"))
	if err != nil {
		return nil, err
	}
	i, err := meter.Float64Histogram(MetricInvocationLength, metric.WithUnit("s"),
		metric.WithDescription("duration of a wrapped nix invocation"))
	if err != nil {
		return nil, err
	}
	return &Recorder{ctx: ctx, tracer: tracer, derivations: d, buildDuration: b, invocation: i}, nil
}

// Invocation records the wrapped command's duration; command is its base name
// and subcommand its bounded operation label (app.Subcommand: an allowlisted
// verb or "other", never a raw user string), both low cardinality; failed is
// true for a non-zero exit. failureKind is the closed set
// nixlog.FailureKinds; it labels the metric for a failed run only, and any
// value outside the set (including "") is recorded as "other" so the label
// can never grow.
func (r *Recorder) Invocation(command, subcommand string, d time.Duration, failed bool, failureKind string) {
	status := "ok"
	attrs := []attribute.KeyValue{
		attribute.String("command", command), attribute.String("subcommand", subcommand),
	}
	if failed {
		status = "error"
		attrs = append(attrs, attribute.String("failure_kind", BoundedFailureKind(failureKind)))
	}
	attrs = append(attrs, attribute.String("status", status))
	r.invocation.Record(r.ctx, seconds(d), metric.WithAttributes(attrs...))
}

// BoundedFailureKind returns k when it is in nixlog.FailureKinds, else "other".
func BoundedFailureKind(k string) string {
	for _, f := range nixlog.FailureKinds {
		if k == f {
			return k
		}
	}
	return nixlog.ErrOther
}

var spanNames = map[nixlog.Kind]string{
	nixlog.KindBuild:      "nix.build",
	nixlog.KindSubstitute: "nix.substitute",
	nixlog.KindWait:       "nix.build.wait",
}

// Emit implements nixlog.Sink.
func (r *Recorder) Emit(a nixlog.Activity) {
	r.metrics(a)
	if a.NoSpan {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("nix.drv.name", a.Name)}
	if a.Path != "" {
		attrs = append(attrs, attribute.String("nix.store_path", a.Path))
	}
	if a.Server != "" {
		attrs = append(attrs, attribute.String("server.address", a.Server))
	}
	if a.Kind == nixlog.KindSubstitute && a.HasBytes {
		attrs = append(attrs, attribute.Int64("nix.substitute.bytes", a.Bytes))
	}
	if a.Placeholder {
		attrs = append(attrs, attribute.Bool("nix.hash_mismatch.placeholder", true))
	}
	_, span := r.tracer.Start(r.ctx, spanNames[a.Kind],
		trace.WithTimestamp(a.Start), trace.WithAttributes(attrs...))
	if a.Failed {
		span.SetAttributes(attribute.String("error.type", a.ErrorType))
		span.SetStatus(codes.Error, a.Reason)
	}
	span.End(trace.WithTimestamp(a.End))
}

func (r *Recorder) metrics(a nixlog.Activity) {
	switch a.Kind {
	case nixlog.KindBuild:
		if a.ErrorType == nixlog.ErrAborted {
			return
		}
		outcome, status := OutcomeBuilt, "ok"
		if a.Failed {
			outcome, status = OutcomeFailed, "error"
		}
		r.derivations.Add(r.ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
		if !a.Instant {
			r.buildDuration.Record(r.ctx, seconds(a.End.Sub(a.Start)),
				metric.WithAttributes(attribute.String("status", status)))
		}
	case nixlog.KindSubstitute:
		if a.ErrorType == nixlog.ErrAborted {
			return
		}
		r.derivations.Add(r.ctx, 1, metric.WithAttributes(attribute.String("outcome", OutcomeSubstituted)))
	}
}

func seconds(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return d.Seconds()
}
