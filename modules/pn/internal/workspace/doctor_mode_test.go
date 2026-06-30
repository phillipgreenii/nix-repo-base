// internal/workspace/doctor_mode_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestWorkspaceMode_Primary(t *testing.T) {
	root := t.TempDir()
	initRealRepo(t, filepath.Join(root, "repo-a"))
	ws := &Workspace{
		root:   root,
		runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}}},
	}
	if m := ws.workspaceMode(context.Background()); m != "primary" {
		t.Fatalf("want primary, got %s", m)
	}
}

func TestWorkspaceMode_Worktree(t *testing.T) {
	base := t.TempDir()
	canonical := filepath.Join(base, "canonical", "repo-a")
	initRealRepo(t, canonical)
	// Create a linked worktree of repo-a under a set dir.
	setRepo := filepath.Join(base, "set", "repo-a")
	runGitT(t, canonical, "worktree", "add", "-q", "-b", "feature", setRepo)
	ws := &Workspace{
		root:   filepath.Join(base, "set"),
		runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"repo-a": {URL: "u", Branch: "main"}}},
	}
	if m := ws.workspaceMode(context.Background()); m != "worktree" {
		t.Fatalf("want worktree, got %s", m)
	}
}
