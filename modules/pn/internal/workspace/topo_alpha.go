package workspace

import "context"

// topoAlpha returns the workspace repos in topological order (dependencies
// before consumers). The 3-tier priority mirrors effectiveLock:
//
//  1. Disk lock matches config → return lock.Order directly (no nix eval).
//  2. effectiveLock succeeds → return derived lock.Order.
//  3. Any error → fall back to alphabetical orderedRepoNames.
//
// The fallback guarantees a deterministic order even when nix is unavailable
// (e.g., in tests that don't provision fake nix responses).
func (ws *Workspace) topoAlpha(ctx context.Context) []string {
	// Tier 1: disk lock is current.
	if ws.lock != nil && lockMatchesConfig(ws.lock, ws.config) {
		if len(ws.lock.Order) > 0 {
			return ws.lock.Order
		}
		// Lock matches but has no order (e.g., single-repo or no edges):
		// fall through to alphabetical so callers always get a non-empty slice.
		return orderedRepoNames(ws.config.Repos)
	}

	// Tier 2: derive a fresh lock.
	lock, _, err := ws.effectiveLock(ctx)
	if err == nil && lock != nil && len(lock.Order) > 0 {
		return lock.Order
	}

	// Tier 3: alphabetical fallback.
	return orderedRepoNames(ws.config.Repos)
}
