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
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock()}
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
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}, lock: emptyLock()}
	env := &doctorEnv{ws: ws, mode: "primary", lock: emptyLock()}
	fs := ws.checkLock(context.Background(), env)
	if !hasFinding(fs, "lock-legacy", SevWarning) {
		t.Fatalf("expected lock-legacy warning, got %+v", fs)
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
