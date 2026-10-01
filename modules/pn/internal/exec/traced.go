package exec

import (
	"context"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// tracedRunner is a decorator that emits one pn.exec span per Run. It
// delegates to inner untouched, so Result buffering, the live tee and error
// values are exactly the inner runner's. With no Telemetry in ctx (or the
// no-op one) the span is a no-op.
type tracedRunner struct {
	inner Runner
}

// Run implements Runner.
func (r *tracedRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	ctx, end := telemetry.StartExec(ctx, name)
	res, err := r.inner.Run(ctx, name, args, opts)
	end(res.ExitCode, err)
	return res, err
}

// WithTracing wraps inner in the pn.exec decorator.
func WithTracing(inner Runner) Runner {
	return &tracedRunner{inner: inner}
}
