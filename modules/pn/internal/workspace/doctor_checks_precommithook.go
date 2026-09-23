// internal/workspace/doctor_checks_precommithook.go
package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// checkPreCommitHookLive guards against a repo silently losing its
// git-triggered pre-commit gate (bd tc-0wzp/tc-771m): 5 of this workspace's 8
// repos lost their local .git/hooks/pre-commit to a prek-version regression,
// and commits succeeded without the hook ever firing — "silently skipping
// validation rather than failing loudly." It was found by accident while
// working an unrelated bead, not by any audit, and `pn workspace doctor` had
// no check that could have caught it (bd tc-wdwnl).
//
// Applies only to a repo that DECLARES .pre-commit-config.yaml — any file at
// that path, live or not, is the repo opting into pre-commit (ADR 0016). A
// repo with no such file has nothing for this check to assert about, so it
// produces no finding.
//
// For a repo that does declare it, .git/hooks/pre-commit — the actual
// git-triggered artifact, a DIFFERENT thing from the .pre-commit-config.yaml
// symlink checkRuffPin's ruff-pin Skipped finding inspects — MUST exist, be
// executable, and (when it happens to be a symlink) not be a dangling/stale
// reference into a garbage-collected /nix/store path. The last of those
// reuses symlinkLiveInNixStore (nix_hooks.go), the exact predicate
// preCommitConfigLive already applies to the generated config, generalized
// to an arbitrary path rather than reimplemented here.
//
// Severity follows the precedent this exact shape of problem already has in
// this codebase — a condition that degrades silently rather than failing
// loudly, where the detection itself must not become a new hard-failure mode
// — set by checkRuffPin's ruff-pin-absent finding (doctor_checks_ruffpin.go)
// and ratified by GAP 3's ruling in bd tc-atsmj: Skipped: true, so the
// finding is fully visible in human/--json output (DoctorReport.Skipped,
// renderHuman) but NEVER fails the exit code, not even under --strict (see
// doctor.go's ExitCode doc and hasAny/HasErrors, both of which exclude
// Skipped findings unconditionally).
func (ws *Workspace) checkPreCommitHookLive(_ context.Context, _ *doctorEnv) []Finding {
	var out []Finding
	for _, name := range orderedRepoNames(ws.config.Repos) {
		repoDir := filepath.Join(ws.root, name)
		if !isGitRepo(repoDir) {
			continue
		}
		if f := ws.preCommitHookLiveFinding(name, repoDir); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// preCommitConfigDeclared reports whether repoDir declares
// .pre-commit-config.yaml — i.e. any file (symlink or not, live or dangling)
// exists at that path. This is the applicability gate: whether the config
// itself currently resolves live is irrelevant to whether the repo opted
// into pre-commit in the first place.
func preCommitConfigDeclared(repoDir string) bool {
	_, err := os.Lstat(filepath.Join(repoDir, preCommitConfigName))
	return err == nil
}

// preCommitHookLiveFinding audits one repo's git-triggered hook, returning
// nil when the repo does not declare .pre-commit-config.yaml or its hook is
// live.
func (ws *Workspace) preCommitHookLiveFinding(repo, repoDir string) *Finding {
	if !preCommitConfigDeclared(repoDir) {
		return nil
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	reason := preCommitHookDeadReason(hookPath)
	if reason == "" {
		return nil
	}
	return &Finding{
		CheckID: "pre-commit-hook-live", Repo: repo, Severity: SevError, Skipped: true,
		Message: fmt.Sprintf(
			"repo %q declares %s but %s %s; commits will silently skip pre-commit validation instead of failing loudly (bd tc-0wzp/tc-771m) — run `nix run .#install-pre-commit-hooks` and re-run doctor",
			repo, preCommitConfigName, hookPath, reason,
		),
		Manual: fmt.Sprintf("regenerate it, then re-run doctor:  (cd %s && nix run .#install-pre-commit-hooks)", repoDir),
	}
}

// preCommitHookDeadReason reports why hookPath is not a live, git-triggered
// pre-commit hook, or "" when it is fine. A symlink hook is checked for
// staleness via symlinkLiveInNixStore, the same primitive preCommitConfigLive
// uses for the generated .pre-commit-config.yaml — the failure mode bd
// tc-0wzp documents (a dangling symlink to a garbage-collected /nix/store
// path). The ordinary case observed in this workspace is a plain (non-symlink)
// generated shell script, which this leaves untouched once it exists and is
// executable.
func preCommitHookDeadReason(hookPath string) string {
	lst, err := os.Lstat(hookPath)
	if err != nil {
		return "is missing"
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		if !symlinkLiveInNixStore(hookPath) {
			return "is a dangling/stale symlink (its /nix/store target has been garbage-collected, or it does not resolve into /nix/store at all)"
		}
		lst, err = os.Stat(hookPath) // resolve the symlink for the executable-bit check below
		if err != nil {
			return "is a dangling/stale symlink"
		}
	}
	if lst.Mode()&0o111 == 0 {
		return "is not executable"
	}
	return ""
}
