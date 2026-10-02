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
// (low cardinality) and failed is true for a non-zero exit.
func (r *Recorder) Invocation(command string, d time.Duration, failed bool) {
	status := "ok"
	if failed {
		status = "error"
	}
	r.invocation.Record(r.ctx, seconds(d), metric.WithAttributes(
		attribute.String("command", command), attribute.String("status", status),
	))
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
