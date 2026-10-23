package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// overridesFixture is a three-repo workspace: consumer "a" depends on "b" (alias
// base) and "c" (alias extra); the lock is preset so no nix evaluation runs.
func overridesFixture(t *testing.T, mkC bool) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	initRealRepo(t, filepath.Join(root, "a"))
	mkRepoDir(t, root, "b")
	if mkC {
		mkRepoDir(t, root, "c")
	}
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
			{Consumer: "a", Alias: "extra", Target: "c"},
			{Consumer: "a", Alias: "base", Target: "b"},
			{Consumer: "b", Alias: "a-back", Target: "a"},
		},
		Order: []string{"c", "b", "a"},
	}
	return openWSWithLock(t, root, toml, lock), root
}

func TestRepoOverrides_ByKeyIsTheSetFlakeCheckInjects(t *testing.T) {
	w, root := overridesFixture(t, true)
	key, got, err := w.RepoOverrides(context.Background(), "a")
	if err != nil {
		t.Fatalf("RepoOverrides: %v", err)
	}
	if key != "a" {
		t.Errorf("key = %q, want a", key)
	}
	want := []string{
		"--override-input", "base", "git+file://" + filepath.Join(root, "b"),
		"--override-input", "extra", "git+file://" + filepath.Join(root, "c"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v\nwant   %v", got, want)
	}
	// The single derivation: identical to what flake-check builds for repo a.
	if fc := w.overrideInputArgsFor("a", overrideOpts{ExcludeRepo: "a"}); !reflect.DeepEqual(got, fc) {
		t.Errorf("verb output %v differs from flake-check's override set %v", got, fc)
	}
}

func TestRepoOverrides_RepoDoesNotOverrideItself(t *testing.T) {
	w, root := overridesFixture(t, true)
	_, got, err := w.RepoOverrides(context.Background(), "b")
	if err != nil {
		t.Fatalf("RepoOverrides: %v", err)
	}
	want := []string{"--override-input", "a-back", "git+file://" + filepath.Join(root, "a")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestRepoOverrides_MissingSiblingCloneIsLeftOut(t *testing.T) {
	w, root := overridesFixture(t, false)
	_, got, err := w.RepoOverrides(context.Background(), "a")
	if err != nil {
		t.Fatalf("RepoOverrides: %v", err)
	}
	want := []string{"--override-input", "base", "git+file://" + filepath.Join(root, "b")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v (no override for the sibling whose clone is absent)", got, want)
	}
}

func TestRepoOverrides_AcceptsAPathInsideTheRepoAndItsWorktrees(t *testing.T) {
	w, root := overridesFixture(t, true)
	inside := filepath.Join(root, "a", "sub", "dir")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", ".worktrees", "drain-x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{filepath.Join(root, "a"), inside, nested} {
		key, _, err := w.RepoOverrides(context.Background(), arg)
		if err != nil || key != "a" {
			t.Errorf("RepoOverrides(%q) = key %q, err %v; want key a", arg, key, err)
		}
	}
}

func TestRepoOverrides_ResolvesAWorktreeKeptOutsideTheRepoThroughItsCommonDir(t *testing.T) {
	w, root := overridesFixture(t, true)
	wt := filepath.Join(t.TempDir(), "elsewhere-wt")
	runGitT(t, filepath.Join(root, "a"), "worktree", "add", "-q", "-b", "wt-branch", wt)
	key, got, err := w.RepoOverrides(context.Background(), wt)
	if err != nil {
		t.Fatalf("RepoOverrides(%s): %v", wt, err)
	}
	if key != "a" || len(got) != 6 {
		t.Errorf("key = %q, args = %v; want repo a with its two overrides", key, got)
	}
}

func TestRepoOverrides_RejectsAPathThatIsNoWorkspaceRepo(t *testing.T) {
	w, _ := overridesFixture(t, true)
	for _, arg := range []string{"", "nope", t.TempDir()} {
		_, _, err := w.RepoOverrides(context.Background(), arg)
		if !errors.Is(err, ErrNotWorkspaceRepo) {
			t.Errorf("RepoOverrides(%q) err = %v, want ErrNotWorkspaceRepo", arg, err)
		}
	}
}

func TestRepoOverrides_WritesNothingToTheWorkspace(t *testing.T) {
	w, root := overridesFixture(t, true)
	before := dirNames(t, root)
	if _, _, err := w.RepoOverrides(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if after := dirNames(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("workspace root changed: before %v, after %v (no lock may be written)", before, after)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}
