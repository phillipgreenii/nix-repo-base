// internal/workspace/doctor_checks_flakelock.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkFlakeLockFresh verifies each consumer's flake.lock pins every workspace
// edge to refRev(target). The per-alias rev read is nix-free; the edge set comes
// from env.lock (effective — may have been derived via nix when the disk lock
// was stale). The fix delegates to `pn workspace update` (relock→commit→push).
func (ws *Workspace) checkFlakeLockFresh(ctx context.Context, env *doctorEnv) []Finding {
	if env.lock == nil {
		return nil
	}
	var fs []Finding
	staleConsumers := map[string]bool{}

	for consumer := range ws.config.Repos {
		consumerDir := filepath.Join(ws.root, consumer)
		if !isGitRepo(consumerDir) {
			continue
		}
		aliases := workspaceAliasesFromLock(env.lock, consumer)
		if len(aliases) == 0 {
			continue
		}
		flakeRel := ws.resolveFlakePath(consumer)
		if flakeRel == "" {
			continue
		}
		lockPath := filepath.Join(consumerDir, filepath.Dir(flakeRel), "flake.lock")
		locked, err := readAliasRevs(lockPath, aliases)
		if err != nil {
			continue
		}
		for _, alias := range aliases {
			target := ws.edgeTarget(env.lock, consumer, alias)
			if target == "" {
				continue
			}
			if env.skipped[target] {
				fs = append(fs, Finding{CheckID: "flake-lock-fresh", Repo: consumer, Severity: SevError,
					Skipped: true, Message: fmt.Sprintf("freshness of input %q skipped (remote of %q unresolved)", alias, target)})
				continue
			}
			want := env.refRev[target]
			got := locked[alias]
			if want == "" || got == "" {
				continue
			}
			if got != want {
				staleConsumers[consumer] = true
				fs = append(fs, Finding{
					CheckID: "flake-lock-fresh", Repo: consumer, Severity: SevError,
					Message: fmt.Sprintf("flake.lock input %q (→ %q) pins %s but %q is at %s", alias, target, short(got), target, short(want)),
				})
			}
		}
	}

	// Attach a single update-delegating fix to the first finding per consumer.
	attachFlakeLockFix(ws, fs, staleConsumers)
	return fs
}

func (ws *Workspace) edgeTarget(lock *Lock, consumer, alias string) string {
	for _, e := range lock.Edges {
		if e.Consumer == consumer && e.Alias == alias {
			return e.Target
		}
	}
	return ""
}

// attachFlakeLockFix marks the first flake-lock-fresh finding fixable; the fix
// runs `pn workspace update` (the proven relock→commit→push, topo-ordered flow).
// This is the ONLY fix that pushes — acceptable per spec decision 9.
func attachFlakeLockFix(ws *Workspace, fs []Finding, stale map[string]bool) {
	done := map[string]bool{}
	for i := range fs {
		if fs[i].CheckID != "flake-lock-fresh" || fs[i].Skipped {
			continue
		}
		c := fs[i].Repo
		fs[i].Manual = "pn workspace update"
		if stale[c] && !done[c] {
			done[c] = true
			fs[i].Fixable = true
			fs[i].fix = func(ctx context.Context) error {
				return ws.Update(ctx, osStderr(), UpdateOptions{InPlace: true})
			}
		}
	}
}
