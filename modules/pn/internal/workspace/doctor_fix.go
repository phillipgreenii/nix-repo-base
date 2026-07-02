// internal/workspace/doctor_fix.go
package workspace

import (
	"context"
	"fmt"
	"sort"
)

// fixOrder ranks a check's fix in the dependency order required for a coherent
// world: clone -> reconcile -> switch -> ff-pull -> lock -> flake-lock(update).
func fixOrder(checkID string) int {
	switch checkID {
	case "repos-present":
		return 0
	case "repos-extra":
		return 1
	case "branch-current", "branch-uniform":
		return 2
	case "branch-synced":
		return 3
	case "lock-present", "lock-legacy", "lock-current", "flake-path-resolves":
		return 4
	case "flake-lock-fresh":
		return 5
	default:
		return 9
	}
}

// applyFixes applies the fixable findings (dependency order). On DryRun it only
// records report.Plan. Otherwise it runs each fix, then re-runs all checks and
// replaces report.Findings with the residual set (so the caller sees what's left).
func applyFixes(ctx context.Context, env *doctorEnv, report *DoctorReport, opts DoctorOptions) {
	fixable := make([]Finding, 0, len(report.Findings))
	for _, f := range report.Findings {
		if f.Fixable && f.fix != nil && !f.Skipped {
			fixable = append(fixable, f)
		}
	}
	sort.SliceStable(fixable, func(i, j int) bool {
		return fixOrder(fixable[i].CheckID) < fixOrder(fixable[j].CheckID)
	})

	if opts.DryRun {
		for _, f := range fixable {
			label := f.CheckID
			if f.Repo != "" {
				label += " (" + f.Repo + ")"
			}
			report.Plan = append(report.Plan, "would fix: "+label+" — "+planAction(f.CheckID))
		}
		return
	}

	var fixErrs []Finding
	for _, f := range fixable {
		if err := f.fix(ctx); err != nil {
			fixErrs = append(fixErrs, Finding{
				CheckID: "fix-failed", Repo: f.Repo, Severity: SevError,
				Message: fmt.Sprintf("fixing %s failed: %v", f.CheckID, err),
			})
		}
	}

	// Recompute env before the re-run: ff-pull/update may have changed
	// remote/local state that refRev and lock captured before fixes ran.
	env.refRev, env.skipped = env.ws.resolveRefRevs(ctx, env.mode, env.offline)
	if l, _, err := env.ws.effectiveLock(ctx); err == nil {
		env.lock = l
	}

	// Re-run all checks against a freshly recomputed view of the workspace.
	residual := runChecks(ctx, env, env.ws.registerChecks())

	// report.Fixed = fixable findings resolved = those attempted whose
	// (CheckID, Repo) no longer appears as a non-skipped finding in the residual
	// re-run. A finding still present after its fix ran was not actually resolved
	// (e.g. a rejected push) and must not be counted.
	report.Fixed = countResolved(fixable, residual)

	residual = append(residual, fixErrs...)
	sortFindings(residual)
	report.Findings = residual
	report.Skipped = collectSkipped(residual)
}

// countResolved returns how many of the attempted fixable findings no longer
// appear (by CheckID+Repo, non-skipped) in the residual re-run.
func countResolved(attempted, residual []Finding) int {
	stillPresent := map[string]bool{}
	for _, f := range residual {
		if !f.Skipped {
			stillPresent[f.CheckID+"\x00"+f.Repo] = true
		}
	}
	n := 0
	for _, f := range attempted {
		if !stillPresent[f.CheckID+"\x00"+f.Repo] {
			n++
		}
	}
	return n
}

// planAction returns the existing command a fix delegates to (for --dry-run).
func planAction(checkID string) string {
	switch checkID {
	case "repos-present":
		return "pn workspace clone"
	case "repos-extra":
		return "pn workspace init (reconcile)"
	case "branch-current", "branch-uniform":
		return "git switch <branch>"
	case "branch-synced":
		return "git merge --ff-only origin/<branch>"
	case "lock-present", "lock-legacy", "lock-current", "flake-path-resolves":
		return "pn workspace lock (WriteDerivedLock)"
	case "flake-lock-fresh":
		return "pn workspace update (relock→commit→push)"
	default:
		return "(delegated fix)"
	}
}
