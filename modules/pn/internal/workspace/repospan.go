package workspace

import (
	"context"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
)

// inRepoSpan runs fn inside a pn.repo span for the named repo and ends the
// span with fn's result (so a failing repo is marked Error). fn receives the
// span-bearing context, so every subprocess it starts nests its pn.exec span
// under this repo. With telemetry off the span is a no-op and fn simply runs.
//
// Per-repo verb loops use it with an early-exit-friendly shape: inside fn,
// `continue` becomes `return nil` and a loop-aborting error is returned, then
// re-checked by the caller.
func inRepoSpan(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	ctx, end := telemetry.StartRepo(ctx, name)
	var err error
	defer func() { end(err) }()
	err = fn(ctx)
	return err
}
