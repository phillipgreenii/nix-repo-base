package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLock_WriteAndRead_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	original := &Lock{
		Repos: map[string]LockedRepo{
			"nix-repo-base": {URL: "github:phillipgreenii/nix-repo-base", Rev: "abc1234567890"},
			"nix-overlay":   {URL: "github:phillipgreenii/nix-overlay", Rev: "deadbeef00000"},
		},
	}
	path := filepath.Join(tmp, "pn-workspace.lock")
	if err := WriteLock(path, original); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	loaded, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(loaded.Repos) != 2 {
		t.Errorf("expected 2 entries, got %d", len(loaded.Repos))
	}
	if loaded.Repos["nix-repo-base"].Rev != "abc1234567890" {
		t.Errorf("rev mismatch: %q", loaded.Repos["nix-repo-base"].Rev)
	}
}

func TestReadLock_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "missing.lock")
	lock, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock should succeed on missing file (empty lock), got %v", err)
	}
	if lock == nil {
		t.Fatal("expected non-nil empty lock")
	}
	if len(lock.Repos) != 0 {
		t.Errorf("expected empty Repos, got %d entries", len(lock.Repos))
	}
}

func TestWriteLock_DeterministicOrdering(t *testing.T) {
	tmp := t.TempDir()
	lock := &Lock{
		Repos: map[string]LockedRepo{
			"z": {URL: "u", Rev: "r"},
			"a": {URL: "u", Rev: "r"},
			"m": {URL: "u", Rev: "r"},
		},
	}
	path := filepath.Join(tmp, "pn-workspace.lock")
	if err := WriteLock(path, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	// Re-write and confirm byte-identical (deterministic ordering).
	path2 := filepath.Join(tmp, "pn-workspace.2.lock")
	if err := WriteLock(path2, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	a, errA := os.ReadFile(path)
	b, errB := os.ReadFile(path2)
	if errA != nil || errB != nil {
		t.Fatalf("ReadFile errors: %v %v", errA, errB)
	}
	if string(a) != string(b) {
		t.Errorf("WriteLock should produce byte-identical output for the same input")
	}
}
