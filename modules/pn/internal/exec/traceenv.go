package exec

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// traceparentEnv is the W3C trace-context environment variable.
const traceparentEnv = "TRACEPARENT"

// traceEnvRunner is a decorator that exports TRACEPARENT, taken from the
// current pn.exec span, into the environment of the child processes that opt in
// with RunOptions.PropagateTrace (hooks and update-locks.sh). Any nix they run
// then nests under the pn.verb trace (ADR 0028). It sits INSIDE the tracing
// decorator so the span context in ctx is the pn.exec span, and it never
// touches argv (the nix wrapper decorator is the only argv rewriter).
//
// It FAILS OPEN: when the call is not marked PropagateTrace, telemetry is off,
// or ctx carries no valid span, it delegates name, args and opts untouched, so
// the child's environment and output are byte-identical to before.
type traceEnvRunner struct {
	inner Runner
}

// WithTraceEnv wraps inner in the TRACEPARENT environment decorator.
func WithTraceEnv(inner Runner) Runner {
	return &traceEnvRunner{inner: inner}
}

// Run implements Runner.
func (r *traceEnvRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	if opts.PropagateTrace && telemetry.From(ctx).Enabled() {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			// Copy: the caller's map must not be mutated. A TRACEPARENT the
			// caller set explicitly is replaced, since pn's span is the parent.
			env := make(map[string]string, len(opts.Env)+1)
			for k, v := range opts.Env {
				env[k] = v
			}
			env[traceparentEnv] = fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())
			opts.Env = env
		}
	}
	return r.inner.Run(ctx, name, args, opts)
}
