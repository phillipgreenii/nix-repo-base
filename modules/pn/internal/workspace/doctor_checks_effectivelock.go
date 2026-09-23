// internal/workspace/doctor_checks_effectivelock.go
package workspace

import "fmt"

// effectiveLockDerivationFinding reports that ws.effectiveLock's own
// derivation failed (bd tc-b3wxl, confirmed gap from tc-dywnx's sweep).
// Doctor() calls ws.effectiveLock(ctx) once, best-effort, to populate
// doctorEnv.lock for every nil-safe downstream check to consume — but until
// now a derivation failure there (`effLock, _, _ := ws.effectiveLock(ctx)`)
// was silently swallowed, so any check consuming env.lock just produced
// FEWER findings with no signal that derivation itself failed, as opposed to
// a check legitimately finding nothing wrong. The exact same underlying
// mechanism (deriveLock -> gatherInputURLs / buildEdges — most concretely
// buildEdges' duplicate_remote_url) already degrades hook execution at
// runtime with a warning (nix_hooks.go's "warning: hook overrides: effective
// lock unavailable (%v); gates may build against locked inputs") — this is
// the doctor-side counterpart of that exact same failure, finally surfaced
// to the audit command instead of only to interactive hook runs.
//
// Unlike nix-cache-trusted/pre-commit-hook-live, effectiveLock is a
// workspace-scoped operation — deriveLock runs at most once per Doctor()
// call, never once per repo — so this is a single workspace-level Finding
// (Repo: "") constructed directly at the one call site in Doctor(), not a
// per-repo check registered in registerChecks(). Forcing it into the
// per-repo check shape would misrepresent its actual scope.
//
// Severity mirrors the same "degrades silently rather than failing loudly"
// precedent nix-cache-trusted/pre-commit-hook-live already established,
// ratified by GAP 3's ruling in bd tc-atsmj: SevError, Skipped: true, so the
// finding is fully visible in human/--json output (DoctorReport.Skipped,
// renderHuman) but NEVER fails the exit code, not even under --strict
// (doctor.go's ExitCode doc and hasAny/HasErrors both exclude Skipped
// findings unconditionally).
func effectiveLockDerivationFinding(err error) Finding {
	return Finding{
		CheckID: "effective-lock-derivable", Severity: SevError, Skipped: true,
		Message: fmt.Sprintf(
			"effective lock could not be derived: %v; checks that consume the effective lock (env.lock) may report fewer findings than they otherwise would as a result, distinct from those checks legitimately finding nothing wrong — the same failure mode nix_hooks.go warns about as \"effective lock unavailable\" at hook-execution time",
			err,
		),
		Manual: "fix the underlying issue (e.g. a duplicate remote URL across workspace repos, or a flake that fails to evaluate), then re-run `pn workspace lock` and doctor",
	}
}
