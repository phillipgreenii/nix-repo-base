package exec

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// traceparentEnv is the W3C trace-context environment variable.
const traceparentEnv = "TRACEPARENT"

// Environment variables a PropagateTrace child (update-locks.sh) reads to run
// its own nix calls under pg-nix-log-wrapped. They are set ONLY when the
// wrapper is usable, so a child that finds them unset runs bare nix as before.
// The wrapper is not on PATH (it is deliberately not in home.packages), so the
// path has to travel with the trace context.
const (
	// EnvNixWrapper is the absolute path of pg-nix-log-wrapped.
	EnvNixWrapper = "PN_NIX_LOG_WRAPPER"
	// EnvNixWrapperEndpoint is the OTLP endpoint pn resolved, passed to the
	// wrapper as --otlp-endpoint.
	EnvNixWrapperEndpoint = "PN_NIX_LOG_OTLP_ENDPOINT"
)

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
	// usable reports whether a wrapper path is an executable regular file
	// (stat in production).
	usable func(string) bool
}

// WithTraceEnv wraps inner in the TRACEPARENT environment decorator.
func WithTraceEnv(inner Runner) Runner {
	return &traceEnvRunner{inner: inner, usable: isExecutableFile}
}

// Run implements Runner.
func (r *traceEnvRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	if opts.PropagateTrace && telemetry.From(ctx).Enabled() {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			// Copy: the caller's map must not be mutated. A TRACEPARENT the
			// caller set explicitly is replaced, since pn's span is the parent.
			env := make(map[string]string, len(opts.Env)+3)
			for k, v := range opts.Env {
				env[k] = v
			}
			env[traceparentEnv] = fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())
			if path, endpoint, ok := userWrapper(ctx, r.usable); ok {
				env[EnvNixWrapper] = path
				env[EnvNixWrapperEndpoint] = endpoint
			}
			opts.Env = env
		}
	}
	return r.inner.Run(ctx, name, args, opts)
}
