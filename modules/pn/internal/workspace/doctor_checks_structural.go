// internal/workspace/doctor_checks_structural.go
package workspace

import (
	"context"
	"path/filepath"
)

// checkLock emits lock-present / lock-legacy / lock-current findings.
//
//	lock-present : warning when no lock.json on disk (effectiveLock derives it).
//	lock-legacy  : warning when the legacy pn-workspace.lock file is present.
//	lock-current : ERROR only when the disk lock's repo-set matches config (so
//	               effectiveLock + overrideInputArgsFor consume it as-is) but its
//	               edges/order differ from a fresh derive; otherwise no finding.
func (ws *Workspace) checkLock(ctx context.Context, env *doctorEnv) []Finding {
	var fs []Finding
	lockPath := filepath.Join(ws.root, LockFileName)

	if !fileExists(lockPath) {
		fs = append(fs, Finding{
			CheckID: "lock-present", Severity: SevWarning,
			Message: "pn-workspace.lock.json is absent (the DAG is derived dynamically)",
			Fixable: true,
			fix:     func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
		})
	}
	if fileExists(filepath.Join(ws.root, LockFileNameLegacy)) {
		fs = append(fs, Finding{
			CheckID: "lock-legacy", Severity: SevWarning,
			Message: "legacy pn-workspace.lock present; superseded by pn-workspace.lock.json",
			Fixable: true,
			fix:     func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
		})
	}

	// lock-current: only meaningful when a disk lock exists and matches config.
	if ws.lock != nil && len(ws.lock.Repos) > 0 && lockMatchesConfig(ws.lock, ws.config) {
		fresh, _, err := deriveLock(ctx, ws, "")
		if err == nil && fresh != nil {
			if !lockEdgesEqual(ws.lock.Edges, fresh.Edges) || !stringSliceEqual(ws.lock.Order, fresh.Order) {
				fs = append(fs, Finding{
					CheckID: "lock-current", Severity: SevError,
					Message: "pn-workspace.lock.json is stale (edges/order differ from a fresh derive) and is consumed as-is",
					Fixable: true,
					fix:     func(c context.Context) error { return ws.WriteDerivedLock(c, ws.root) },
				})
			}
		}
	}
	return fs
}

// lockEdgesEqual reports whether a and b contain the same edges in the same
// order. Compared by length-then-element rather than reflect.DeepEqual so a
// nil slice (the zero value of "var edges []LockEdge" when buildEdges finds
// no edges — the fresh-derive path) and a non-nil empty slice (emptyLock's
// `Edges: []LockEdge{}` — what filterLock starts from when writing a
// `--repos` subset's on-disk lock) compare equal. reflect.DeepEqual treats
// those as DIFFERENT, which made lock-current fire spuriously for any
// `--repos` subset workforest set whose members have zero dependency edges
// between each other (e.g. two otherwise-unrelated repos subsetted together
// because a change touches both) — a semantically identical "no edges" lock,
// not staleness. LockEdge is a plain comparable struct (three strings).
func lockEdgesEqual(a, b []LockEdge) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stringSliceEqual is lockEdgesEqual's counterpart for Order, guarding
// against the same nil-vs-empty-slice mismatch.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
