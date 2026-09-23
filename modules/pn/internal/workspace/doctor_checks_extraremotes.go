// internal/workspace/doctor_checks_extraremotes.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
)

// checkExtraRemotesSynced guards a gap branch-synced cannot see by
// construction (bd tc-t6bv0, confirmed gap from tc-dywnx's sweep):
// resolveRefRevs (doctor_refrev.go) compares local HEAD against exactly ONE
// URL -- flakeURLToHTTPS(displayURL(rc)), the pn-workspace.toml-declared
// remote -- never against the repo's actual `git remote -v` list. Live
// evidence from this workspace: homelab's canonical clone has THREE git
// remotes -- origin (a native git multi-pushurl fanout that already covers
// both twistcone hosts in one `git push`), a dedicated synfra remote, and a
// wholly separate bitbucket remote (a deliberate offsite backup mirror, the
// same pattern as the personal-repo-to-Forgejo mirroring bd tc-5i6ux covers)
// -- while pn-workspace.toml declares only the git.twistcone.us URL.
//
// Design decision (the judgment call this bead asks for): enumerate EVERY
// named git remote and check each one NOT already covered by the
// TOML-declared URL(s), rather than special-casing "the push remote" or
// "remotes named X". Reasoning:
//
//  1. resolvePushRemote (push.go) picks exactly ONE remote NAME per push
//     invocation -- it does not iterate every configured remote, and a
//     multi-pushurl remote's OWN individual pushurls are opaque to it (one
//     `git push origin` either succeeds or fails as a whole; a partial
//     mid-fanout failure on one pushurl is invisible to both push and, until
//     now, doctor). So push's own notion of "the repo's remotes" is
//     NARROWER than the full remote list -- it is not a source doctor can
//     defer to for "which remotes matter."
//  2. Any git remote NOT named by the TOML is, by definition, something `pn`
//     itself never manages -- push won't reach it via the convention chain
//     unless a human names it explicitly (--remote/pushRemote/pushDefault).
//     Its existence is either a deliberate manual mirror (bitbucket-style) or
//     a leftover the operator forgot about; doctor cannot tell which, so it
//     must not FAIL on staleness (a human-only workflow silently drifting is
//     expected, not a bug) -- it can only make the drift VISIBLE. Hence
//     Skipped:true (never fails the exit code, not even --strict), mirroring
//     the established precedent for this exact shape of problem
//     (checkNixCacheTrusted, checkPreCommitHookLive; ratified by GAP 3's
//     ruling in bd tc-atsmj).
//  3. A NEW check (rather than extending branch-synced's own loop) was
//     chosen over folding into checkBranches: branch-synced's finding is
//     driven by env.refRev/env.skipped, precomputed ONCE per repo in
//     resolveRefRevs against the single TOML URL and deliberately
//     documented (see doctor_refrev.go, branchSyncedFinding) as a two-source
//     consistency invariant -- classification and the trigger must read the
//     SAME sha. Bolting a second, per-remote ls-remote loop onto that
//     function would multiply findings under the SAME CheckID with a
//     DIFFERENT data source (a live per-remote ls-remote, not env.refRev),
//     which is exactly the kind of two-source drift that comment warns
//     against. A separate CheckID with its own, independently-documented
//     data source keeps the two invariants apart and keeps branch-synced's
//     existing tests (and its fix machinery -- fast-forward pull/push)
//     completely undisturbed. push behavior itself is explicitly out of
//     scope (this bead is doctor visibility only).
//
// Applicability: primary mode only (mirrors branch-synced itself, dropped in
// worktree mode -- checkBranches' doc comment) and skipped entirely offline
// (mirrors resolveRefRevs' own offline skip; no network call is attempted).
// A repo with no extra remotes (the common case) produces no finding at all.
func (ws *Workspace) checkExtraRemotesSynced(ctx context.Context, env *doctorEnv) []Finding {
	if env.mode != "primary" || env.offline {
		return nil
	}
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		dir := filepath.Join(ws.root, name)
		if !isGitRepo(dir) {
			continue
		}
		rc := ws.config.Repos[name]
		branch := rc.Branch
		if branch == "" {
			branch = "main"
		}
		local, err := captureHead(ctx, ws.runner, dir)
		if err != nil {
			continue // no local HEAD to compare against; branch-current/tree-clean already cover this
		}
		remotes, err := readGitRemotes(ctx, ws.runner, dir)
		if err != nil || len(remotes) == 0 {
			continue
		}
		out = append(out, ws.extraRemotesFindingsForRepo(ctx, name, dir, branch, local, rc, remotes)...)
	}
	return out
}

// extraRemotesFindingsForRepo audits every git remote of dir NOT matching (by
// canonicalURL) any of rc's TOML-declared URLs -- the exclusion required by
// this bead so branch-synced's own URL is never double-reported.
func (ws *Workspace) extraRemotesFindingsForRepo(
	ctx context.Context, name, dir, branch, local string, rc RepoConfig, remotes map[string]string,
) []Finding {
	declared := map[string]bool{}
	for _, u := range declaredRemoteURLs(rc) {
		declared[canonicalURL(u)] = true
	}

	var extraNames []string
	for rn := range remotes {
		if declared[canonicalURL(remotes[rn])] {
			continue
		}
		extraNames = append(extraNames, rn)
	}
	sort.Strings(extraNames)

	var out []Finding
	for _, rn := range extraNames {
		url := remotes[rn]
		if f := ws.extraRemoteSyncedFinding(ctx, name, dir, rn, url, branch, local); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// declaredRemoteURLs returns every remote URL pn-workspace.toml declares for
// rc -- the single r.URL form, or every entry in the multi-remote r.Remotes
// form. This is the exclusion set checkExtraRemotesSynced subtracts before
// enumerating "extra" remotes, so a remote already covered by branch-synced
// (single-URL form) or repo-identity (multi-remote form) is never
// double-reported here.
func declaredRemoteURLs(rc RepoConfig) []string {
	if len(rc.Remotes) > 0 {
		urls := make([]string, 0, len(rc.Remotes))
		for _, rm := range rc.Remotes {
			urls = append(urls, rm.URL)
		}
		return urls
	}
	if rc.URL != "" {
		return []string{rc.URL}
	}
	return nil
}

// extraRemoteSyncedFinding compares local HEAD against remote name/url's live
// `git ls-remote` head for branch, returning nil when they match (in sync) or
// when the remote could not be resolved at all (bare ls-remote failure, e.g.
// a stale/unreachable mirror URL -- surfaced as its own Skipped finding, not
// silently ignored: an unresolvable extra remote is exactly the kind of
// "silently falls behind" gap this bead exists to close).
//
// No Fixable/fix is attached and no --fix path exists for this CheckID:
// per the design decision above, `pn` deliberately does not manage remotes
// outside the TOML-declared one(s) -- offering an auto-fix here would imply
// pn intends to push there, which push.go itself is out of scope to do.
func (ws *Workspace) extraRemoteSyncedFinding(ctx context.Context, repo, dir, remoteName, url, branch, local string) *Finding {
	sha := ws.lsRemoteHead(ctx, url, branch)
	if sha == "" {
		return &Finding{
			CheckID: "extra-remotes-synced", Repo: repo, Severity: SevError, Skipped: true,
			Message: fmt.Sprintf(
				"repo %q remote %q (%s) could not be resolved (git ls-remote failed or returned no refs/heads/%s) -- its sync state is unknown",
				repo, remoteName, url, branch,
			),
		}
	}
	if sha == local {
		return nil
	}
	return &Finding{
		CheckID: "extra-remotes-synced", Repo: repo, Severity: SevError, Skipped: true,
		Message: fmt.Sprintf(
			"repo %q remote %q (%s) HEAD %s != local HEAD %s -- this remote is not covered by pn-workspace.toml or `pn workspace push`'s resolved push remote, so it can silently fall behind; sync it by hand if it is meant to track this branch",
			repo, remoteName, url, short(sha), short(local),
		),
		Manual: fmt.Sprintf("git -C %s push %s %s", dir, remoteName, branch),
	}
}
