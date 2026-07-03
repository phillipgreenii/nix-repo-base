package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLock_WriteAndRead_RoundTrip verifies that a fully-populated Lock
// serialises and deserialises without loss.
func TestLock_WriteAndRead_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	original := &Lock{
		Terminal: "homelab",
		Order:    []string{"nix-repo-base", "nix-overlay", "homelab"},
		Repos: map[string]LockRepoEntry{
			"homelab":       {FlakePath: "nix/flake.nix", RemoteURL: "ssh://git@synfra.twistcone.us:222/twistcone/homelab.git"},
			"nix-repo-base": {FlakePath: "flake.nix", RemoteURL: "github:phillipgreenii/nix-repo-base"},
			"nix-overlay":   {FlakePath: "flake.nix", RemoteURL: "github:phillipgreenii/nix-overlay"},
		},
		Edges: []LockEdge{
			{Consumer: "homelab", Alias: "phillipgreenii-nix-base", Target: "nix-repo-base"},
			{Consumer: "nix-overlay", Alias: "phillipgreenii-nix-base", Target: "nix-repo-base"},
		},
	}

	path := filepath.Join(tmp, LockFileName)
	if err := WriteLock(path, original); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	loaded, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if loaded.Terminal != original.Terminal {
		t.Errorf("Terminal round-trip: got %q, want %q", loaded.Terminal, original.Terminal)
	}
	if !reflect.DeepEqual(loaded.Order, original.Order) {
		t.Errorf("Order round-trip: got %v, want %v", loaded.Order, original.Order)
	}
	if !reflect.DeepEqual(loaded.Repos, original.Repos) {
		t.Errorf("Repos round-trip mismatch")
	}
	if !reflect.DeepEqual(loaded.Edges, original.Edges) {
		t.Errorf("Edges round-trip: got %v, want %v", loaded.Edges, original.Edges)
	}
}

// TestWriteLock_DeterministicOrdering verifies that writing the same Lock twice
// produces byte-identical output.
func TestWriteLock_DeterministicOrdering(t *testing.T) {
	tmp := t.TempDir()
	lock := &Lock{
		Terminal: "z",
		Order:    []string{"a", "m", "z"},
		Repos: map[string]LockRepoEntry{
			"z": {FlakePath: "flake.nix", RemoteURL: "github:o/z"},
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
			"m": {FlakePath: "flake.nix", RemoteURL: "github:o/m"},
		},
		Edges: []LockEdge{
			{Consumer: "z", Alias: "a-inp", Target: "a"},
			{Consumer: "m", Alias: "a-inp", Target: "a"},
		},
	}

	path1 := filepath.Join(tmp, "lock1.json")
	path2 := filepath.Join(tmp, "lock2.json")
	if err := WriteLock(path1, lock); err != nil {
		t.Fatalf("WriteLock 1: %v", err)
	}
	if err := WriteLock(path2, lock); err != nil {
		t.Fatalf("WriteLock 2: %v", err)
	}
	a, errA := os.ReadFile(path1)
	b, errB := os.ReadFile(path2)
	if errA != nil || errB != nil {
		t.Fatalf("ReadFile errors: %v %v", errA, errB)
	}
	if string(a) != string(b) {
		t.Errorf("WriteLock should produce byte-identical output for the same input;\ngot1:\n%s\ngot2:\n%s", a, b)
	}
}

// TestReadLock_MissingFile verifies ReadLock returns a non-nil empty Lock when
// the .json file does not exist (and no legacy file is present).
func TestReadLock_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	lock, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock should succeed on missing file (empty lock), got %v", err)
	}
	if lock == nil {
		t.Fatal("expected non-nil empty lock")
	}
	if len(lock.Order) != 0 || len(lock.Repos) != 0 || len(lock.Edges) != 0 {
		t.Errorf("expected empty lock, got order=%v repos=%v edges=%v", lock.Order, lock.Repos, lock.Edges)
	}
}

// TestReadLock_LegacyFileEmitsNotice verifies that if the old pn-workspace.lock
// file exists but the new .json file does not, ReadLock prints a migration
// notice to stderr.
func TestReadLock_LegacyFileEmitsNotice(t *testing.T) {
	tmp := t.TempDir()
	// Write a legacy lock file.
	legacyPath := filepath.Join(tmp, LockFileNameLegacy)
	if err := os.WriteFile(legacyPath, []byte(`{"order":["a"],"dependsOn":{}}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	newPath := filepath.Join(tmp, LockFileName)

	// Capture stderr by redirecting temporarily.
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	lock, readErr := ReadLock(newPath)

	_ = w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	stderrOutput := string(buf[:n])

	if readErr != nil {
		t.Fatalf("ReadLock on missing new file with legacy present: %v", readErr)
	}
	if lock == nil {
		t.Fatal("expected non-nil empty lock")
	}
	if !strings.Contains(stderrOutput, LockFileNameLegacy) {
		t.Errorf("expected migration notice mentioning %q in stderr; got: %q", LockFileNameLegacy, stderrOutput)
	}
	if !strings.Contains(stderrOutput, LockFileName) {
		t.Errorf("expected migration notice mentioning %q in stderr; got: %q", LockFileName, stderrOutput)
	}
}

// TestParseLock_SelfEdge verifies invariant (a): no self-edge.
func TestParseLock_SelfEdge(t *testing.T) {
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
		},
		Edges: []LockEdge{
			{Consumer: "a", Alias: "a-inp", Target: "a"},
		},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for self-edge, got nil")
	}
	if !strings.Contains(err.Error(), "self-edge") {
		t.Errorf("expected 'self-edge' in error, got: %v", err)
	}
}

// TestParseLock_DuplicateAlias verifies invariant (b): per-consumer alias uniqueness.
func TestParseLock_DuplicateAlias(t *testing.T) {
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"},
			"target1":  {FlakePath: "flake.nix", RemoteURL: "github:o/target1"},
			"target2":  {FlakePath: "flake.nix", RemoteURL: "github:o/target2"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "shared-alias", Target: "target1"},
			{Consumer: "consumer", Alias: "shared-alias", Target: "target2"},
		},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for duplicate alias, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected 'duplicate' in error, got: %v", err)
	}
}

// TestParseLock_EdgeConsumerMissingFromRepos verifies invariant (c): consumer exists.
func TestParseLock_EdgeConsumerMissingFromRepos(t *testing.T) {
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"target": {FlakePath: "flake.nix", RemoteURL: "github:o/target"},
		},
		Edges: []LockEdge{
			{Consumer: "missing-consumer", Alias: "inp", Target: "target"},
		},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for missing consumer, got nil")
	}
	if !strings.Contains(err.Error(), "missing-consumer") {
		t.Errorf("expected consumer name in error, got: %v", err)
	}
}

// TestParseLock_EdgeTargetMissingFromRepos verifies invariant (c): target exists.
func TestParseLock_EdgeTargetMissingFromRepos(t *testing.T) {
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "inp", Target: "missing-target"},
		},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if !strings.Contains(err.Error(), "missing-target") {
		t.Errorf("expected target name in error, got: %v", err)
	}
}

// TestParseLock_EdgeTargetEmptyFlakePath verifies invariant (d): target has a flake path.
func TestParseLock_EdgeTargetEmptyFlakePath(t *testing.T) {
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"},
			"target":   {FlakePath: "", RemoteURL: "github:o/target"}, // no flake_path
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "inp", Target: "target"},
		},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for target with empty flake_path, got nil")
	}
	if !strings.Contains(err.Error(), "flake_path") {
		t.Errorf("expected 'flake_path' in error, got: %v", err)
	}
}

// TestParseLock_TerminalNotInRepos verifies invariant (e): terminal exists in repos.
func TestParseLock_TerminalNotInRepos(t *testing.T) {
	lock := &Lock{
		Terminal: "nonexistent",
		Repos: map[string]LockRepoEntry{
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
		},
		Edges: []LockEdge{},
	}
	err := ParseLock(lock)
	if err == nil {
		t.Fatal("expected error for terminal not in repos, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected terminal name in error, got: %v", err)
	}
}

// TestParseLock_ValidLock verifies a valid lock passes all invariants.
func TestParseLock_ValidLock(t *testing.T) {
	lock := &Lock{
		Terminal: "homelab",
		Order:    []string{"nix-repo-base", "homelab"},
		Repos: map[string]LockRepoEntry{
			"homelab":       {FlakePath: "nix/flake.nix", RemoteURL: "ssh://git@host/homelab.git"},
			"nix-repo-base": {FlakePath: "flake.nix", RemoteURL: "github:phillipgreenii/nix-repo-base"},
		},
		Edges: []LockEdge{
			{Consumer: "homelab", Alias: "phillipgreenii-nix-base", Target: "nix-repo-base"},
		},
	}
	if err := ParseLock(lock); err != nil {
		t.Errorf("expected no error for valid lock, got: %v", err)
	}
}

// TestReadLock_InvalidLock_SelfEdge verifies that ReadLock returns an error when
// the on-disk lock violates invariant (a): no self-edge.
func TestReadLock_InvalidLock_SelfEdge(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "order": ["a"],
  "repos": {"a": {"flake_path": "flake.nix", "remote_url": "github:o/a"}},
  "edges": [{"consumer": "a", "alias": "a-inp", "target": "a"}]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with self-edge, got nil")
	}
	if !strings.Contains(err.Error(), "self-edge") {
		t.Errorf("expected 'self-edge' in error, got: %v", err)
	}
}

// TestReadLock_InvalidLock_DuplicateAlias verifies that ReadLock returns an
// error when the on-disk lock violates invariant (b): per-consumer alias
// uniqueness.
func TestReadLock_InvalidLock_DuplicateAlias(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "order": ["consumer", "target1", "target2"],
  "repos": {
    "consumer": {"flake_path": "flake.nix", "remote_url": "github:o/consumer"},
    "target1":  {"flake_path": "flake.nix", "remote_url": "github:o/target1"},
    "target2":  {"flake_path": "flake.nix", "remote_url": "github:o/target2"}
  },
  "edges": [
    {"consumer": "consumer", "alias": "shared", "target": "target1"},
    {"consumer": "consumer", "alias": "shared", "target": "target2"}
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with duplicate alias, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected 'duplicate' in error, got: %v", err)
	}
}

// TestReadLock_InvalidLock_ConsumerMissingFromRepos verifies that ReadLock
// returns an error when the on-disk lock violates invariant (c): edge consumer
// must exist in repos.
func TestReadLock_InvalidLock_ConsumerMissingFromRepos(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "order": ["target"],
  "repos": {"target": {"flake_path": "flake.nix", "remote_url": "github:o/target"}},
  "edges": [{"consumer": "ghost-consumer", "alias": "inp", "target": "target"}]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with missing consumer, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-consumer") {
		t.Errorf("expected consumer name in error, got: %v", err)
	}
}

// TestReadLock_InvalidLock_TargetMissingFromRepos verifies that ReadLock
// returns an error when the on-disk lock violates invariant (c): edge target
// must exist in repos.
func TestReadLock_InvalidLock_TargetMissingFromRepos(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "order": ["consumer"],
  "repos": {"consumer": {"flake_path": "flake.nix", "remote_url": "github:o/consumer"}},
  "edges": [{"consumer": "consumer", "alias": "inp", "target": "ghost-target"}]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with missing target, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-target") {
		t.Errorf("expected target name in error, got: %v", err)
	}
}

// TestReadLock_InvalidLock_TargetEmptyFlakePath verifies that ReadLock returns
// an error when the on-disk lock violates invariant (d): edge target must have
// a non-empty flake_path.
func TestReadLock_InvalidLock_TargetEmptyFlakePath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "order": ["consumer", "target"],
  "repos": {
    "consumer": {"flake_path": "flake.nix", "remote_url": "github:o/consumer"},
    "target":   {"remote_url": "github:o/target"}
  },
  "edges": [{"consumer": "consumer", "alias": "inp", "target": "target"}]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with empty flake_path, got nil")
	}
	if !strings.Contains(err.Error(), "flake_path") {
		t.Errorf("expected 'flake_path' in error, got: %v", err)
	}
}

// TestReadLock_InvalidLock_TerminalNotInRepos verifies that ReadLock returns an
// error when the on-disk lock violates invariant (e): terminal must exist in
// repos.
func TestReadLock_InvalidLock_TerminalNotInRepos(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "terminal": "ghost-terminal",
  "order": ["a"],
  "repos": {"a": {"flake_path": "flake.nix", "remote_url": "github:o/a"}},
  "edges": []
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := ReadLock(path)
	if err == nil {
		t.Fatal("expected error for disk-loaded lock with terminal not in repos, got nil")
	}
	if !strings.Contains(err.Error(), "ghost-terminal") {
		t.Errorf("expected terminal name in error, got: %v", err)
	}
}

// TestReadLock_ValidLock_PassesInvariantCheck verifies that a valid on-disk
// lock loads without error after ParseLock validation is applied.
func TestReadLock_ValidLock_PassesInvariantCheck(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, LockFileName)
	content := `{
  "terminal": "homelab",
  "order": ["nix-repo-base", "homelab"],
  "repos": {
    "homelab":       {"flake_path": "nix/flake.nix", "remote_url": "ssh://git@host/homelab.git"},
    "nix-repo-base": {"flake_path": "flake.nix",     "remote_url": "github:o/nix-repo-base"}
  },
  "edges": [
    {"consumer": "homelab", "alias": "nrb", "target": "nix-repo-base"}
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	lock, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock should succeed for valid lock, got: %v", err)
	}
	if lock.Terminal != "homelab" {
		t.Errorf("terminal: got %q, want %q", lock.Terminal, "homelab")
	}
}

// TestWriteLock_EdgesSorted verifies that WriteLock sorts edges by
// (consumer, alias, target) regardless of input order.
func TestWriteLock_EdgesSorted(t *testing.T) {
	tmp := t.TempDir()
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"b": {FlakePath: "flake.nix", RemoteURL: "github:o/b"},
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
			"c": {FlakePath: "flake.nix", RemoteURL: "github:o/c"},
		},
		// Provide edges in reverse sorted order.
		Edges: []LockEdge{
			{Consumer: "c", Alias: "inp", Target: "a"},
			{Consumer: "b", Alias: "inp", Target: "a"},
			{Consumer: "a", Alias: "zzz", Target: "b"},
			{Consumer: "a", Alias: "aaa", Target: "b"},
		},
	}
	path := filepath.Join(tmp, LockFileName)
	if err := WriteLock(path, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	loaded, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	want := []LockEdge{
		{Consumer: "a", Alias: "aaa", Target: "b"},
		{Consumer: "a", Alias: "zzz", Target: "b"},
		{Consumer: "b", Alias: "inp", Target: "a"},
		{Consumer: "c", Alias: "inp", Target: "a"},
	}
	if !reflect.DeepEqual(loaded.Edges, want) {
		t.Errorf("edges not sorted: got %v, want %v", loaded.Edges, want)
	}
}
