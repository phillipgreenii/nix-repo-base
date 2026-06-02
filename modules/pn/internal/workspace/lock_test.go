package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLock_WriteAndRead_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	original := &Lock{
		Order: []string{"base", "overlay", "ziprecruiter"},
		DependsOn: map[string][]string{
			"overlay":      {"base"},
			"ziprecruiter": {"base", "overlay"},
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
	if len(loaded.Order) != 3 || loaded.Order[0] != "base" || loaded.Order[2] != "ziprecruiter" {
		t.Errorf("order round-trip mismatch: %v", loaded.Order)
	}
	got := loaded.DependsOn["ziprecruiter"]
	if len(got) != 2 || got[0] != "base" || got[1] != "overlay" {
		t.Errorf("dependsOn[ziprecruiter] = %v, want [base overlay]", got)
	}
}

func TestWriteLock_OmitsURLsAndRevs(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "pn-workspace.lock")
	if err := WriteLock(path, &Lock{
		Order:     []string{"base", "term"},
		DependsOn: map[string][]string{"term": {"base"}},
	}); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	for _, banned := range []string{"url", "rev", "github", "git@"} {
		if strings.Contains(s, banned) {
			t.Errorf("lock must not contain %q; got:\n%s", banned, s)
		}
	}
	for _, want := range []string{"order", "dependsOn"} {
		if !strings.Contains(s, want) {
			t.Errorf("lock should contain %q; got:\n%s", want, s)
		}
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
	if len(lock.Order) != 0 || len(lock.DependsOn) != 0 {
		t.Errorf("expected empty lock, got order=%v dependsOn=%v", lock.Order, lock.DependsOn)
	}
}

func TestWriteLock_DeterministicOrdering(t *testing.T) {
	tmp := t.TempDir()
	lock := &Lock{
		Order:     []string{"a", "m", "z"},
		DependsOn: map[string][]string{"z": {"a"}, "m": {"a"}},
	}
	path := filepath.Join(tmp, "pn-workspace.lock")
	if err := WriteLock(path, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
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
