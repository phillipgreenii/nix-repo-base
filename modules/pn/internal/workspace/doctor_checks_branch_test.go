// internal/workspace/doctor_checks_branch_test.go
package workspace

import (
	"context"
	"path/filepath"
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
