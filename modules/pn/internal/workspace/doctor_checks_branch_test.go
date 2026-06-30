// internal/workspace/doctor_checks_branch_test.go
package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckBranches_WrongBranchIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	fs := ws.checkBranches(context.Background(), env)
	if !hasFindingForRepo(fs, "branch-current", "dep", SevError) {
		t.Fatalf("wrong branch should be error: %+v", fs)
	}
}

func TestCheckBranches_DirtyIsErrorPrimaryWarningWorktree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	dirtyTrackedFile(t, dir, "README.md", "changed\n")
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}
	ws := &Workspace{root: root, runner: exec.NewRealRunner(), config: cfg}

	envP := &doctorEnv{ws: ws, mode: "primary", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envP), "tree-clean", "dep", SevError) {
		t.Fatal("dirty primary should be error")
	}
	envW := &doctorEnv{ws: ws, mode: "worktree", refRev: map[string]string{}, skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), envW), "tree-clean", "dep", SevWarning) {
		t.Fatal("dirty worktree should be warning")
	}
}

func TestCheckBranches_AheadOfRemoteIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}}}
	// refRev (remote) differs from local HEAD => not synced.
	env := &doctorEnv{ws: ws, mode: "primary",
		refRev:  map[string]string{"dep": "0000000000000000000000000000000000000000"},
		skipped: map[string]bool{}}
	if !hasFindingForRepo(ws.checkBranches(context.Background(), env), "branch-synced", "dep", SevError) {
		t.Fatal("local != remote should be branch-synced error")
	}
}

func TestCheckBranches_AheadOnlyIsFixableByPush(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dep")
	initRealRepo(t, dir)
	bare := setupLocalBareRemote(t, dir) // origin = bare; main pushed; origin/main tracks it
	h0 := headRev(t, dir)                // the remote HEAD (what was pushed)
	want := addCommit(t, dir, "ahead.txt", "x", "local ahead") // local now ahead, behind 0

	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: bare, Branch: "main"}}}}
	// refRev = remote HEAD (h0); local HEAD (want) is strictly ahead of it.
	env := &doctorEnv{ws: ws, mode: "primary",
		refRev: map[string]string{"dep": h0}, skipped: map[string]bool{}}

	fs := ws.checkBranches(context.Background(), env)
	var bs *Finding
	for i := range fs {
		if fs[i].CheckID == "branch-synced" && fs[i].Repo == "dep" {
			bs = &fs[i]
		}
	}
	if bs == nil || bs.Severity != SevError {
		t.Fatalf("expected branch-synced error for ahead-only: %+v", fs)
	}
	if !bs.Fixable || bs.fix == nil {
		t.Fatalf("ahead-only branch-synced should be fixable via push: %+v", *bs)
	}
	if !strings.Contains(bs.Manual, "push") {
		t.Fatalf("ahead-only manual hint should mention push, got %q", bs.Manual)
	}
	// Apply the fix: it should fast-forward-push local HEAD to the remote.
	if err := bs.fix(context.Background()); err != nil {
		t.Fatalf("push fix failed: %v", err)
	}
	remoteHead := strings.Fields(runGitT(t, dir, "ls-remote", bare, "refs/heads/main"))[0]
	if remoteHead != want {
		t.Fatalf("push fix did not advance remote: want %s got %s", want, remoteHead)
	}
}
