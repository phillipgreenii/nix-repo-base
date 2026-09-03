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

// gitMutator is the composed role this package's MUTATING git call sites
// need, per design doc pg2-migib §7a's operator-decided full adoption and its
// implementation (bead pg2-f1cq7): Fetcher, WorktreeManager, Syncer,
// Committer, Pusher, BranchLister, RemoteManager. *gitclient.Client satisfies
// it by construction (asserted below). This is pn's mutating-side migration
// (bead pg2-8bfb5) — the counterpart to gitReader's read-side migration
// (bead pg2-oxle0) above.
type gitMutator interface {
	gitclient.Fetcher
	gitclient.WorktreeManager
	gitclient.Syncer
	gitclient.Committer
	gitclient.Pusher
	gitclient.BranchLister
	gitclient.RemoteManager
}

var _ gitMutator = (*gitclient.Client)(nil)

// gitMutatorOpener anchors a gitMutator at dir, mirroring gitOpener — a
// package-level var so tests can substitute a fake without threading a new
// seam through every mutating call site.
//
// gitclient.New (not Discover) is used for the identical reason gitOpener
// uses it: every mutating call site below already knows dir is a repo root.
type gitMutatorOpener func(ctx context.Context, dir string) (gitMutator, error)

var openGitMutator gitMutatorOpener = func(ctx context.Context, dir string) (gitMutator, error) {
	return gitclient.New(ctx, dir)
}

// gitCloner opens a fresh repository at dir by cloning url, mirroring
// gitclient.Clone's signature narrowed to the gitMutator role clone.go needs
// from the result — a package-level var so tests can substitute a fake,
// matching openGitReader/openGitMutator above.
type gitCloner func(ctx context.Context, url, dir string, opts gitclient.CloneOptions) (gitMutator, *gitclient.Handle, error)

var cloneGitRepo gitCloner = func(ctx context.Context, url, dir string, opts gitclient.CloneOptions) (gitMutator, *gitclient.Handle, error) {
	return gitclient.Clone(ctx, url, dir, opts)
}
