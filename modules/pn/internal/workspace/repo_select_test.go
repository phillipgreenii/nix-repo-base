package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// selectFixture is a three-repo workspace: consumer "a" depends on "b" (alias
// base) and "c" (alias extra); the lock is preset so no nix evaluation runs.
func selectFixture(t *testing.T, mkC bool) (*Workspace, string) {
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

func TestResolveRepoKey_ByKey(t *testing.T) {
	w, _ := selectFixture(t, true)
	key, err := w.resolveRepoKey(context.Background(), "b")
	if err != nil || key != "b" {
		t.Errorf("resolveRepoKey(b) = %q, %v; want b", key, err)
	}
}

func TestResolveRepoKey_AcceptsAPathInsideTheRepoAndItsWorktrees(t *testing.T) {
	w, root := selectFixture(t, true)
	inside := filepath.Join(root, "a", "sub", "dir")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", ".worktrees", "drain-x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{filepath.Join(root, "a"), inside, nested} {
		key, err := w.resolveRepoKey(context.Background(), arg)
		if err != nil || key != "a" {
			t.Errorf("resolveRepoKey(%q) = key %q, err %v; want key a", arg, key, err)
		}
	}
}

func TestResolveRepoKey_ResolvesAWorktreeKeptOutsideTheRepoThroughItsCommonDir(t *testing.T) {
	w, root := selectFixture(t, true)
	wt := filepath.Join(t.TempDir(), "elsewhere-wt")
	runGitT(t, filepath.Join(root, "a"), "worktree", "add", "-q", "-b", "wt-branch", wt)
	key, err := w.resolveRepoKey(context.Background(), wt)
	if err != nil || key != "a" {
		t.Errorf("resolveRepoKey(%s) = key %q, err %v; want repo a", wt, key, err)
	}
}

func TestResolveRepoKey_RejectsAPathThatIsNoWorkspaceRepo(t *testing.T) {
	w, _ := selectFixture(t, true)
	for _, arg := range []string{"", "nope", t.TempDir()} {
		_, err := w.resolveRepoKey(context.Background(), arg)
		if !errors.Is(err, ErrNotWorkspaceRepo) {
			t.Errorf("resolveRepoKey(%q) err = %v, want ErrNotWorkspaceRepo", arg, err)
		}
	}
}
