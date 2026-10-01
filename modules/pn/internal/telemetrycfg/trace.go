package telemetrycfg

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// RunState carries per-invocation telemetry facts from where they are learned
// to where they are used. It is created by the CLI entry point, stored in the
// command context, and mutated by whoever starts the root span:
//
//   - the root pn.verb span setup calls SetTraceID with the span's trace id;
//   - the event log (events.jsonl run_start/run_end) and the end-of-run trace
//     hint read it.
//
// A nil *RunState is valid and inert, so library code needs no nil checks.
type RunState struct {
	// Res is the resolved configuration for this run (read-only after
	// NewRunState).
	Res Resolution

	mu      sync.Mutex
	traceID string
}

type runStateKey struct{}

// NewRunState returns a RunState for a run with the given resolution.
func NewRunState(res Resolution) *RunState { return &RunState{Res: res} }

// WithRunState stores s in ctx.
func WithRunState(ctx context.Context, s *RunState) context.Context {
	return context.WithValue(ctx, runStateKey{}, s)
}

// RunStateFrom returns the RunState stored in ctx, or nil.
func RunStateFrom(ctx context.Context) *RunState {
	s, _ := ctx.Value(runStateKey{}).(*RunState)
	return s
}

// SetTraceID records the run's trace id (32 lowercase hex chars). The all-zero
// invalid id is ignored so a no-op tracer leaves the run without a trace id.
func (s *RunState) SetTraceID(id string) {
	if s == nil || id == "" || id == "00000000000000000000000000000000" {
		return
	}
	s.mu.Lock()
	s.traceID = id
	s.mu.Unlock()
}

// TraceID returns the recorded trace id, or "".
func (s *RunState) TraceID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceID
}

// HintEnabled reports whether the end-of-run `trace: <id>` stderr hint is on:
// only under -v or PN_TRACE_HINT=1. stdout is never touched.
func HintEnabled(verbose bool, getenv func(string) string) bool {
	return verbose || getenv(EnvTraceHint) == "1"
}

// WriteHint prints `trace: <id>` to w when the hint is enabled and a trace id
// exists. It returns whether it wrote.
func WriteHint(w io.Writer, s *RunState, verbose bool, getenv func(string) string) bool {
	id := s.TraceID()
	if id == "" || !HintEnabled(verbose, getenv) {
		return false
	}
	_, _ = fmt.Fprintf(w, "trace: %s\n", id)
	return true
}
