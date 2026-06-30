// internal/workspace/doctor_refrev_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func newWS(t *testing.T, root string, repos map[string]RepoConfig) *Workspace {
	t.Helper()
	return &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: repos}}
}

func TestResolveRefRevs_PrimaryUsesRemote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo-a")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir)
	want := headRev(t, dir)
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: bare, Branch: "main"}})
	refRev, skipped := ws.resolveRefRevs(context.Background(), "primary", false)
	if skipped["repo-a"] {
		t.Fatal("repo-a unexpectedly skipped")
	}
	if refRev["repo-a"] != want {
		t.Fatalf("refRev: want %s got %s", want, refRev["repo-a"])
	}
}

func TestResolveRefRevs_OfflineSkips(t *testing.T) {
	root := t.TempDir()
	initRealRepo(t, filepath.Join(root, "repo-a"))
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: "git@x:o/r.git", Branch: "main"}})
	_, skipped := ws.resolveRefRevs(context.Background(), "primary", true)
	if !skipped["repo-a"] {
		t.Fatal("offline: repo-a should be skipped")
	}
}

func TestResolveRefRevs_WorktreeUsesLocalHead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo-a")
	initRealRepo(t, dir)
	want := headRev(t, dir)
	ws := newWS(t, root, map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}})
	refRev, skipped := ws.resolveRefRevs(context.Background(), "worktree", false)
	if skipped["repo-a"] || refRev["repo-a"] != want {
		t.Fatalf("worktree refRev: want %s got %s (skipped=%v)", want, refRev["repo-a"], skipped["repo-a"])
	}
}
