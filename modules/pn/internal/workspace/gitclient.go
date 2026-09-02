// internal/workspace/gitclient.go
package workspace

import (
	"context"

	"github.com/phillipgreenii/x/gitclient"
)

// gitReader is the composed role this package's read-side git call sites
// need: Locator (CommonDir, CurrentBranch, RemoteURL) + RefReader (RefExists,
// HasUpstream) + StatusReader (Status). *gitclient.Client satisfies it by
// construction (asserted below). This is pn's read-side migration onto
// x/gitclient (bead pg2-oxle0, decided by pg2-app6l, per epic pg2-svfbb's
// design section 4.5's pn paragraph): mutating verbs, streaming, and
// per-invocation `-c` config stay on the raw exec.Runner pending pn's own
// full-adoption design pass (pg2-migib) — this bead migrates ONLY call sites
// that map onto a role method exactly (see the per-call-site doc comments at
// each migrated call site for what does and does not qualify).
type gitReader interface {
	gitclient.Locator
	gitclient.RefReader
	gitclient.StatusReader
}

var _ gitReader = (*gitclient.Client)(nil)

// gitOpener anchors a gitReader at dir. A package-level var, not a plain
// function, so tests can substitute a fake without threading a new seam
// through every caller — design section 4.6's app-local opener seam, mirroring
// pg-pr branch's openGit and ccpool's gitfacet.Resolve.
//
// gitclient.New (not Discover) is used: every call site below already knows
// dir is (or should be) a repo root — the same assumption the raw
// `git -C <dir> ...` calls it replaces already made — so anchoring exactly at
// dir, with no upward walk, is the precise behavioral match.
type gitOpener func(ctx context.Context, dir string) (gitReader, error)

var openGitReader gitOpener = func(ctx context.Context, dir string) (gitReader, error) {
	return gitclient.New(ctx, dir)
}
