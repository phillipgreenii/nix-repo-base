// Package telemetrytest builds a telemetry.Telemetry backed by in-memory
// exporters so tests can assert on the spans and metrics pn emits without a
// collector.
package telemetrytest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// Recorder exposes what an in-memory Telemetry captured.
type Recorder struct {
	spans     *tracetest.SpanRecorder
	reader    *sdkmetric.ManualReader
	shutdowns atomic.Int32
	// ShutdownErr, if set before Shutdown, is returned by the shutdown hook
	// (to prove a Shutdown failure never changes the exit code).
	ShutdownErr error
}

// New returns an enabled Telemetry plus its Recorder.
func New() (*telemetry.Telemetry, *Recorder) {
	rec := &Recorder{
		spans:  tracetest.NewSpanRecorder(),
		reader: sdkmetric.NewManualReader(),
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec.spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(rec.reader))
	tel := telemetry.NewWithProviders(tp.Tracer(telemetry.ServiceName), mp.Meter(telemetry.ServiceName),
		func(ctx context.Context) error {
			rec.shutdowns.Add(1)
			return errors.Join(rec.ShutdownErr, tp.Shutdown(ctx))
		})
	return tel, rec
}

// Ended returns the spans that have ended, in end order.
func (r *Recorder) Ended() []sdktrace.ReadOnlySpan { return r.spans.Ended() }

// ByName returns the ended spans with the given name.
func (r *Recorder) ByName(name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range r.spans.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}
	return out
}

// ShutdownCount reports how many times the shutdown hook ran.
func (r *Recorder) ShutdownCount() int { return int(r.shutdowns.Load()) }

// Metrics collects the current metric data.
func (r *Recorder) Metrics(t testing.TB) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}
