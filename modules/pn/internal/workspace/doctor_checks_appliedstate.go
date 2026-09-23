// internal/workspace/doctor_checks_appliedstate.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkAppliedStateCurrent guards a gap doctor cannot see by construction (bd
// tc-g42eq, confirmed gap from tc-dywnx's sweep, tc-wdwnl/tc-3ips5/tc-b3wxl/
// tc-t6bv0's sibling): AppliedState (appliedstate.go) records, per repo, the
// local `git rev-parse HEAD` that was in effect the last time `pn workspace
// apply` actually ran a build over it (AppliedRef, written by markApplied —
// updatecache.go). Nothing outside apply's OWN internal rebuild-skip decision
// (needsRebuild) ever reads that record back and compares it against the
// repo's CURRENT local HEAD — grep confirms doctor has zero references to
// AppliedState/readAppliedState prior to this file, and a differential
// experiment (synthetic AppliedState + an advanced local HEAD + an
// otherwise-in-sync remote, so branch-synced produces no finding) reproduces
// the gap live: `pn workspace doctor` returns nothing naming the divergence.
//
// `pn workspace info` already PUBLISHES AppliedRef per repo (info.go), but
// only as raw data -- it does not compare it against local HEAD, so the
// divergence is discoverable only by a human manually diffing info's output
// against `git log`. This check does that comparison automatically, the same
// service checkBranches' branch-synced already provides for local-vs-remote
// divergence (a structurally identical shape: compare local HEAD against one
// other recorded reference point).
//
// Severity mirrors the established precedent for this shape of problem
// (checkNixCacheTrusted, checkPreCommitHookLive, checkExtraRemotesSynced;
// ratified by GAP 3's ruling in bd tc-atsmj): SevError, Skipped: true, so the
// finding is fully visible in human/--json output but NEVER fails the exit
// code, not even under --strict. This is deliberately NOT branch-synced's
// plain SevError (which DOES gate --strict): unlike a remote (external,
// shared state that push discipline is expected to keep in sync), a local
// commit outrunning the last apply is the NORMAL, moment-to-moment state of
// active development -- every commit made since the last `pn workspace apply`
// trips it, and it self-resolves on the next apply. Flagging it is valuable
// (nothing else tells an operator their running system predates a local
// commit), but treating it as a hard failure would fire on nearly every dev
// machine on nearly every doctor run and train operators to ignore it --
// exactly the anti-pattern markApplied's own fail-closed-warning comment
// reasons about for the analogous locked-revs case.
//
// No Fixable/fix path: the "fix" is `pn workspace apply`, a full
// build+activate, not a cheap git-level operation like the other
// Fixable findings in this package (switch branch, stash, push/pull) --
// doctor --fix does not perform side-effecting system activations.
//
// Applicability: primary mode only (mirrors branch-synced and
// extra-remotes-synced -- AppliedState's keyPath is the canonical
// <root>/<name>, which a worktree set member does not resolve to; dropped
// there the same way branch-synced is). A repo with no AppliedState record at
// all (never applied on this machine, or the applied-state store was cleared)
// is a distinct, explicitly-named Skipped finding, not silence and not a
// false "in sync" -- an absent record carries no evidence either way (the
// exact case this bead's own investigation ran into on the orchestrator's
// machine).
func (ws *Workspace) checkAppliedStateCurrent(ctx context.Context, env *doctorEnv) []Finding {
	if env.mode != "primary" {
		return nil
	}
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		dir := filepath.Join(ws.root, name)
		if !isGitRepo(dir) {
			continue
		}
		local, err := captureHead(ctx, ws.runner, dir)
		if err != nil {
			continue // no local HEAD to compare against; tree-clean/branch-current already cover this
		}
		if f := ws.appliedStateCurrentFinding(name, dir, local); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// appliedStateCurrentFinding compares repoDir's local HEAD against its
// AppliedState.AppliedRef, returning nil when they match (no divergence to
// report), a Skipped finding naming the repo when no record exists, or a
// Skipped finding naming both shas when they diverge.
func (ws *Workspace) appliedStateCurrentFinding(repo, dir, local string) *Finding {
	st, ok, err := readAppliedState(dir)
	if err != nil {
		return &Finding{
			CheckID: "applied-state-current", Repo: repo, Severity: SevError, Skipped: true,
			Message: fmt.Sprintf("repo %q: could not read applied-state record: %v -- staleness cannot be assessed", repo, err),
		}
	}
	if !ok {
		return &Finding{
			CheckID: "applied-state-current", Repo: repo, Severity: SevError, Skipped: true,
			Message: fmt.Sprintf(
				"repo %q has no applied-state record on this machine -- `pn workspace apply` has never recorded an apply for it here, so staleness cannot be assessed",
				repo,
			),
		}
	}
	if st.AppliedRef == local {
		return nil
	}
	return &Finding{
		CheckID: "applied-state-current", Repo: repo, Severity: SevError, Skipped: true,
		Message: fmt.Sprintf(
			"repo %q local HEAD %s has diverged from the last applied state %s (applied at %s) -- the currently active system may not reflect this repo's latest local commit(s)",
			repo, short(local), short(st.AppliedRef), st.AppliedAt,
		),
		Manual: "pn workspace apply",
	}
}
