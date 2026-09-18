// internal/workspace/doctor_checks_structural_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckLock_MissingIsWarning(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock(),
	}
	env := &doctorEnv{ws: ws, mode: "primary", lock: emptyLock()}
	fs := ws.checkLock(context.Background(), env)
	if !hasFinding(fs, "lock-present", SevWarning) {
		t.Fatalf("expected lock-present warning, got %+v", fs)
	}
}

func TestCheckLock_LegacyIsWarning(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, LockFileNameLegacy), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// also write a current lock so lock-present passes
	if err := os.WriteFile(filepath.Join(root, LockFileName), []byte(`{"order":[],"repos":{},"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock(),
	}
	env := &doctorEnv{ws: ws, mode: "primary", lock: emptyLock()}
	fs := ws.checkLock(context.Background(), env)
	if !hasFinding(fs, "lock-legacy", SevWarning) {
		t.Fatalf("expected lock-legacy warning, got %+v", fs)
	}
}

// TestCheckLock_SubsetWithNoInternalEdgesIsNotStale reproduces pg2-jj4ie: a
// `--repos` subset workforest set whose two members have no dependency edge
// on EACH OTHER (their shared dependency, the workspace terminal, is
// excluded from the subset) has an on-disk lock written by filterLock, which
// starts from emptyLock()'s non-nil `Edges: []LockEdge{}` and never appends
// (zero cross-member edges) — so it stays that literal non-nil empty slice.
// checkLock's own fresh re-derive (deriveLock -> buildEdges) instead leaves
// `var edges []LockEdge` untouched (nil) when it finds nothing to append.
// Before the fix, reflect.DeepEqual(nil, []LockEdge{}) was false, so
// lock-current fired even though both locks agree there are no edges.
func TestCheckLock_SubsetWithNoInternalEdgesIsNotStale(t *testing.T) {
	root := t.TempDir()
	cfg := &WorkspaceConfig{
		// workspace.terminal cleared by filterConfig: the real terminal
		// (e.g. the ziprecruiter repo) is not a member of this subset.
		Repos: map[string]RepoConfig{
			"agent-support": {URL: "u1", Branch: "main"},
			"support-apps":  {URL: "u2", Branch: "main"},
		},
	}
	// The subset's on-disk lock, shaped exactly as filterLock produces it:
	// same repo keys as config (so lockMatchesConfig passes), Order carries
	// both members, Edges is the non-nil-but-empty slice emptyLock() seeds
	// (neither repo consumes the other as a flake input in this subset).
	onDisk := &Lock{
		Order: []string{"agent-support", "support-apps"},
		Repos: map[string]LockRepoEntry{
			"agent-support": {RemoteURL: "u1"},
			"support-apps":  {RemoteURL: "u2"},
		},
		Edges: []LockEdge{},
	}
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(), config: cfg, lock: onDisk}
	env := &doctorEnv{ws: ws, mode: "worktree", lock: onDisk}

	fs := ws.checkLock(context.Background(), env)
	for _, f := range fs {
		if f.CheckID == "lock-current" {
			t.Fatalf("subset lock with zero internal edges must not be flagged stale: %+v", f)
		}
	}
}

// TestLockEdgesEqual_NilVsEmptyAreEqual pins the nil-vs-empty-slice
// equivalence directly (the mechanical bug reflect.DeepEqual had).
func TestLockEdgesEqual_NilVsEmptyAreEqual(t *testing.T) {
	if !lockEdgesEqual(nil, []LockEdge{}) {
		t.Fatal("nil and empty []LockEdge must compare equal")
	}
	if !stringSliceEqual(nil, []string{}) {
		t.Fatal("nil and empty []string must compare equal")
	}
	if lockEdgesEqual([]LockEdge{{Consumer: "a", Alias: "b", Target: "c"}}, nil) {
		t.Fatal("a populated slice must not compare equal to nil/empty")
	}
}

// hasFinding is a shared test predicate (define once here).
func hasFinding(fs []Finding, id string, sev Severity) bool {
	for _, f := range fs {
		if f.CheckID == id && f.Severity == sev {
			return true
		}
	}
	return false
}
