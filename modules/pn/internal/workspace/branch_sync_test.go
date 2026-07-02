// internal/workspace/branch_sync_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestSwitchToDefaultBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.switchToDefaultBranch(context.Background(), dir, "main"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if b := currentBranch(t, dir); b != "main" {
		t.Fatalf("want main, got %s", b)
	}
}

func TestSwitchToDefaultBranch_RefusesDirty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	runGitT(t, dir, "switch", "-q", "-c", "feature")
	dirtyTrackedFile(t, dir, "README.md", "changed\n")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.switchToDefaultBranch(context.Background(), dir, "main"); err == nil {
		t.Fatal("expected refusal on dirty tree")
	}
}

func TestFastForwardIfBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	initRealRepo(t, dir)
	setupLocalBareRemote(t, dir)
	// Advance the remote via a second clone, then reset local behind.
	want := addCommit(t, dir, "b.txt", "y", "add b")
	runGitT(t, dir, "push", "-q", "origin", "main")
	runGitT(t, dir, "reset", "-q", "--hard", "HEAD~1")
	runGitT(t, dir, "fetch", "-q", "origin")
	ws := &Workspace{runner: exec.NewRealRunner()}
	if err := ws.fastForwardIfBehind(context.Background(), dir, "origin", "main"); err != nil {
		t.Fatalf("ff: %v", err)
	}
	if got := headRev(t, dir); got != want {
		t.Fatalf("ff did not advance to remote: want %s got %s", want, got)
	}
}
