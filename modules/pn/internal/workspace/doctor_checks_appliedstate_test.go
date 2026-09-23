// internal/workspace/doctor_checks_appliedstate_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

// appliedStateWorkspace builds a one-repo workspace whose repo is a real git
// repo. Every test that needs an AppliedState record writes one directly via
// writeAppliedState, keyed by dir -- exactly the shape appliedStateKeyPath
// (apply.go) produces (canonical <root>/<name>) and the round-trip test in
// appliedstate_test.go already establishes. XDG_DATA_HOME is always pointed
// at an isolated t.TempDir(), never the real applied-state store.
func appliedStateWorkspace(t *testing.T) (ws *Workspace, dir string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	dir = filepath.Join(root, "apps")
	initRealRepo(t, dir)
	ws = newWS(t, root, map[string]RepoConfig{
		"apps": {URL: dir + ".git", Branch: "main"},
	})
	return ws, dir
}

// TestCheckAppliedStateCurrent_NoRecordIsSkippedFindingNamingRepo covers the
// case this bead's own investigation hit on the orchestrator's machine: no
// AppliedState record exists at all (apply has never run here). Must be a
// named, visible Skipped finding -- not silence (silence would be
// indistinguishable from "verified in sync").
func TestCheckAppliedStateCurrent_NoRecordIsSkippedFindingNamingRepo(t *testing.T) {
	ws, _ := appliedStateWorkspace(t)

	fs := ws.checkAppliedStateCurrent(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "applied-state-current", "apps") {
		t.Fatalf("expected a Skipped applied-state-current finding when no record exists; got %+v", fs)
	}
	f := findingByID(t, fs, "applied-state-current")
	if !contains([]byte(f.Message), "no applied-state record") {
		t.Errorf("message should explain no record exists: %q", f.Message)
	}
}

// TestCheckAppliedStateCurrent_MatchingRecordProducesNoFinding covers the
// baseline this bead must not regress: an AppliedState record whose
// AppliedRef equals local HEAD (the repo has not moved since the last apply)
// produces nothing.
func TestCheckAppliedStateCurrent_MatchingRecordProducesNoFinding(t *testing.T) {
	ws, dir := appliedStateWorkspace(t)
	head := headRev(t, dir)
	if err := writeAppliedState(dir, AppliedState{
		Schema:     appliedStateSchema,
		AppliedRef: head,
		AppliedAt:  "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("writeAppliedState: %v", err)
	}

	fs := ws.checkAppliedStateCurrent(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if len(fs) != 0 {
		t.Fatalf("a repo whose local HEAD matches AppliedRef must produce no finding; got %+v", fs)
	}
}

// TestCheckAppliedStateCurrent_DivergedRecordIsSkippedFindingNamingBothShas is
// the bead's core live scenario (differentially observed in the investigation
// transcript): local HEAD has advanced past the last apply's AppliedRef via a
// plain new commit. Must surface a Skipped (never hard-failing) finding
// naming both shas, with a Manual `pn workspace apply` suggestion.
func TestCheckAppliedStateCurrent_DivergedRecordIsSkippedFindingNamingBothShas(t *testing.T) {
	ws, dir := appliedStateWorkspace(t)
	appliedSha := headRev(t, dir)
	if err := writeAppliedState(dir, AppliedState{
		Schema:     appliedStateSchema,
		AppliedRef: appliedSha,
		AppliedAt:  "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("writeAppliedState: %v", err)
	}
	newSha := addCommit(t, dir, "extra.txt", "more\n", "advance past last apply")

	fs := ws.checkAppliedStateCurrent(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	if !hasSkippedFinding(t, fs, "applied-state-current", "apps") {
		t.Fatalf("expected a Skipped applied-state-current finding for a diverged repo; got %+v", fs)
	}
	f := findingByID(t, fs, "applied-state-current")
	if !contains([]byte(f.Message), short(newSha)) || !contains([]byte(f.Message), short(appliedSha)) {
		t.Errorf("message should name both the local HEAD %s and the applied ref %s: %q", short(newSha), short(appliedSha), f.Message)
	}
	if f.Manual != "pn workspace apply" {
		t.Errorf("Manual = %q, want %q", f.Manual, "pn workspace apply")
	}
	if f.Fixable {
		t.Error("must not be Fixable: the fix is a full build+activate, not a cheap git-level operation doctor --fix performs")
	}
}

// TestCheckAppliedStateCurrent_DirtyDoesNotSuppressComparison covers that an
// uncommitted (dirty) tracked change does not stop this check from comparing
// HEAD -- tree-clean (checkBranches) already flags dirtiness separately; this
// check is orthogonal and must still run.
func TestCheckAppliedStateCurrent_DirtyDoesNotSuppressComparison(t *testing.T) {
	ws, dir := appliedStateWorkspace(t)
	appliedSha := headRev(t, dir)
	if err := writeAppliedState(dir, AppliedState{
		Schema:     appliedStateSchema,
		AppliedRef: appliedSha,
		AppliedAt:  "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("writeAppliedState: %v", err)
	}
	dirtyTrackedFile(t, dir, "README.md", "changed\n")

	fs := ws.checkAppliedStateCurrent(context.Background(), &doctorEnv{ws: ws, mode: "primary"})
	// HEAD did not move (only the working tree is dirty), so AppliedRef still
	// matches local HEAD -- no finding from THIS check (tree-clean covers the
	// dirtiness itself, in checkBranches, not exercised here).
	if len(fs) != 0 {
		t.Fatalf("a dirty-but-HEAD-unchanged repo must produce no applied-state-current finding; got %+v", fs)
	}
}

// TestCheckAppliedStateCurrent_WorktreeModeSkipsEntirely mirrors
// branch-synced/extra-remotes-synced: AppliedState's keyPath is the canonical
// <root>/<name>, which a worktree set member does not resolve to, so this
// check must not run at all in worktree mode.
func TestCheckAppliedStateCurrent_WorktreeModeSkipsEntirely(t *testing.T) {
	ws, _ := appliedStateWorkspace(t)
	// Deliberately no AppliedState record at all -- if this ran, it would
	// produce a "no record" finding; worktree mode must suppress it entirely.

	fs := ws.checkAppliedStateCurrent(context.Background(), &doctorEnv{ws: ws, mode: "worktree"})
	if len(fs) != 0 {
		t.Fatalf("worktree mode must produce no applied-state-current findings; got %+v", fs)
	}
}

// TestRegisterChecks_IncludesAppliedStateCurrent pins the wiring: the check
// must be present in the default registry doctor.go's registerChecks builds.
func TestRegisterChecks_IncludesAppliedStateCurrent(t *testing.T) {
	ws := &Workspace{}
	found := false
	for _, c := range ws.registerChecks() {
		if c.id == "applied-state-current" {
			found = true
		}
	}
	if !found {
		t.Fatal("registerChecks() must include the applied-state-current check")
	}
}
