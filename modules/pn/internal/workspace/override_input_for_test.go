package workspace

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// openWSWithLock opens a workspace with config TOML and a pre-set lock.
func openWSWithLock(t *testing.T, root, toml string, lock *Lock) *Workspace {
	t.Helper()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if lock != nil {
		w.lock = lock
	}
	return w
}

// TestOverrideInputArgsFor_TwoConsumersDifferentAliases verifies that
// consumer A uses alias "x-base" and consumer C uses alias "different"
// for the same target B.
func TestOverrideInputArgsFor_TwoConsumersDifferentAliases(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "a")
	mkRepoDir(t, root, "b")
	mkRepoDir(t, root, "c")

	toml := `
[repos.a]
url = "github:owner/a"

[repos.b]
url = "github:owner/b"

[repos.c]
url = "github:owner/c"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"a": {FlakePath: "flake.nix", RemoteURL: "github:owner/a"},
			"b": {FlakePath: "flake.nix", RemoteURL: "github:owner/b"},
			"c": {FlakePath: "flake.nix", RemoteURL: "github:owner/c"},
		},
		Edges: []LockEdge{
			{Consumer: "a", Alias: "x-base", Target: "b"},
			{Consumer: "c", Alias: "different", Target: "b"},
		},
		Order: []string{"b", "a", "c"},
	}
	w := openWSWithLock(t, root, toml, lock)

	// Consumer "a": should emit --override-input x-base git+file://<b_dir>
	bDir := filepath.Join(root, "b")
	got := w.overrideInputArgsFor("a", overrideOpts{})
	want := []string{"--override-input", "x-base", "git+file://" + bDir}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overrideInputArgsFor(a): got %v, want %v", got, want)
	}

	// Consumer "c": should emit --override-input different git+file://<b_dir>
	got2 := w.overrideInputArgsFor("c", overrideOpts{})
	want2 := []string{"--override-input", "different", "git+file://" + bDir}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("overrideInputArgsFor(c): got %v, want %v", got2, want2)
	}
}

// TestOverrideInputArgsFor_ExcludeRepo verifies that opts.ExcludeRepo skips
// the edge targeting that repo.
func TestOverrideInputArgsFor_ExcludeRepo(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "consumer")
	mkRepoDir(t, root, "dep1")
	mkRepoDir(t, root, "dep2")

	toml := `
[repos.consumer]
url = "github:owner/consumer"

[repos.dep1]
url = "github:owner/dep1"

[repos.dep2]
url = "github:owner/dep2"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix"},
			"dep1":     {FlakePath: "flake.nix"},
			"dep2":     {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "dep1-alias", Target: "dep1"},
			{Consumer: "consumer", Alias: "dep2-alias", Target: "dep2"},
		},
		Order: []string{"dep1", "dep2", "consumer"},
	}
	w := openWSWithLock(t, root, toml, lock)

	// Exclude dep1 — only dep2 edge should appear.
	dep2Dir := filepath.Join(root, "dep2")
	got := w.overrideInputArgsFor("consumer", overrideOpts{ExcludeRepo: "dep1"})
	want := []string{"--override-input", "dep2-alias", "git+file://" + dep2Dir}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overrideInputArgsFor with ExcludeRepo: got %v, want %v", got, want)
	}
}

// TestOverrideInputArgsFor_MissingTargetDirSkipped verifies that a target
// without a clone directory on disk is silently skipped.
func TestOverrideInputArgsFor_MissingTargetDirSkipped(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "consumer")
	// "dep" dir intentionally absent

	toml := `
[repos.consumer]
url = "github:owner/consumer"

[repos.dep]
url = "github:owner/dep"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix"},
			"dep":      {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "dep-alias", Target: "dep"},
		},
		Order: []string{"dep", "consumer"},
	}
	w := openWSWithLock(t, root, toml, lock)

	got := w.overrideInputArgsFor("consumer", overrideOpts{})
	if len(got) != 0 {
		t.Errorf("expected empty overrides (dep dir missing), got %v", got)
	}
}

// TestOverrideInputArgsFor_NoLockEdgesFallsThrough verifies that with no lock
// edges, the result is empty.
func TestOverrideInputArgsFor_NoLockEdgesFallsThrough(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "consumer")
	mkRepoDir(t, root, "dep")

	toml := `
[repos.consumer]
url = "github:owner/consumer"

[repos.dep]
url = "github:owner/dep"
`
	// lock with no edges
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix"},
			"dep":      {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{},
		Order: []string{"dep", "consumer"},
	}
	w := openWSWithLock(t, root, toml, lock)

	got := w.overrideInputArgsFor("consumer", overrideOpts{})
	if len(got) != 0 {
		t.Errorf("expected empty overrides (no edges for consumer), got %v", got)
	}
}

// TestOverrideInputArgsFor_OverridePathUsed verifies that opts.OverridePaths
// replaces the default clone dir.
func TestOverrideInputArgsFor_OverridePathUsed(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "consumer")
	mkRepoDir(t, root, "dep")
	alt := t.TempDir() // stand-in path

	toml := `
[repos.consumer]
url = "github:owner/consumer"

[repos.dep]
url = "github:owner/dep"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix"},
			"dep":      {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "dep-alias", Target: "dep"},
		},
		Order: []string{"dep", "consumer"},
	}
	w := openWSWithLock(t, root, toml, lock)

	got := w.overrideInputArgsFor("consumer", overrideOpts{OverridePaths: map[string]string{"dep": alt}})
	want := []string{"--override-input", "dep-alias", "git+file://" + alt}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overrideInputArgsFor with OverridePaths: got %v, want %v", got, want)
	}
}

// TestWorkspaceInputNamesFromEdges verifies that workspaceInputNamesFromEdges
// returns the aliases for the consumer from the lock edges.
func TestWorkspaceInputNamesFromEdges(t *testing.T) {
	root := t.TempDir()
	toml := `
[repos.terminal]
url = "github:owner/terminal"

[repos.dep1]
url = "github:owner/dep1"

[repos.dep2]
url = "github:owner/dep2"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"terminal": {FlakePath: "flake.nix"},
			"dep1":     {FlakePath: "flake.nix"},
			"dep2":     {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "terminal", Alias: "dep1-alias", Target: "dep1"},
			{Consumer: "terminal", Alias: "dep2-alias", Target: "dep2"},
		},
		Order: []string{"dep1", "dep2", "terminal"},
	}
	w := openWSWithLock(t, root, toml, lock)

	names := w.workspaceInputNamesFromEdges("terminal")
	// Should return the aliases that terminal uses
	if len(names) != 2 {
		t.Fatalf("got %d names, want 2: %v", len(names), names)
	}
	found1, found2 := false, false
	for _, n := range names {
		if n == "dep1-alias" {
			found1 = true
		}
		if n == "dep2-alias" {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Errorf("missing expected aliases in %v", names)
	}
}

// TestWorkspaceDisplayNamesFromEdges verifies that workspaceDisplayNamesFromEdges
// maps alias -> target repo key for the terminal's edges.
func TestWorkspaceDisplayNamesFromEdges(t *testing.T) {
	root := t.TempDir()
	toml := `
[repos.terminal]
url = "github:owner/terminal"

[repos.dep1]
url = "github:owner/dep1"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"terminal": {FlakePath: "flake.nix"},
			"dep1":     {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "terminal", Alias: "dep1-lock-alias", Target: "dep1"},
		},
		Order: []string{"dep1", "terminal"},
	}
	w := openWSWithLock(t, root, toml, lock)

	m := w.workspaceDisplayNamesFromEdges("terminal")
	if m["dep1-lock-alias"] != "dep1" {
		t.Errorf("display name for dep1-lock-alias = %q, want %q", m["dep1-lock-alias"], "dep1")
	}
}

// TestOverrideInputArgsFor_NilLock ensures no panic with empty lock.
func TestOverrideInputArgsFor_NilLock(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "dep")
	w := openWS(t, root, `
[repos.dep]
url = "github:owner/dep"
`)
	// w.lock is an emptyLock() (no edges)
	got := w.overrideInputArgsFor("consumer", overrideOpts{})
	if len(got) != 0 {
		t.Errorf("expected empty from nil lock, got %v", got)
	}
}

// TestOverrideInputArgsFor_SortedByAlias verifies that results are sorted by alias.
func TestOverrideInputArgsFor_SortedByAlias(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "consumer")
	mkRepoDir(t, root, "a")
	mkRepoDir(t, root, "b")
	mkRepoDir(t, root, "c")

	toml := `
[repos.consumer]
url = "github:owner/consumer"
[repos.a]
url = "github:owner/a"
[repos.b]
url = "github:owner/b"
[repos.c]
url = "github:owner/c"
`
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"consumer": {FlakePath: "flake.nix"},
			"a":        {FlakePath: "flake.nix"},
			"b":        {FlakePath: "flake.nix"},
			"c":        {FlakePath: "flake.nix"},
		},
		Edges: []LockEdge{
			{Consumer: "consumer", Alias: "z-alias", Target: "c"},
			{Consumer: "consumer", Alias: "a-alias", Target: "a"},
			{Consumer: "consumer", Alias: "m-alias", Target: "b"},
		},
		Order: []string{"a", "b", "c", "consumer"},
	}
	w := openWSWithLock(t, root, toml, lock)

	got := w.overrideInputArgsFor("consumer", overrideOpts{})
	// Should be sorted by alias: a-alias, m-alias, z-alias
	if len(got) != 9 {
		t.Fatalf("got %d args, want 9: %v", len(got), got)
	}
	// Extract just the alias positions (indices 1, 4, 7)
	aliases := []string{got[1], got[4], got[7]}
	wantAliases := []string{"a-alias", "m-alias", "z-alias"}
	if !reflect.DeepEqual(aliases, wantAliases) {
		t.Errorf("alias ordering: got %v, want %v", aliases, wantAliases)
	}
}
