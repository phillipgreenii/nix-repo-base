package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNeedsRebuild_Force(t *testing.T) {
	w := &Workspace{runner: exec.NewFakeRunner()}
	got, err := w.needsRebuild(context.Background(), []string{"/x"}, true, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("force should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_DirtyTree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte(" M file\n")}, nil)
	w := &Workspace{runner: f}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("dirty tree should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_CleanUnchangedSkips(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	hashDir := filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hashDir, "repo"), []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	out := &bytes.Buffer{}
	got, err := w.needsRebuild(context.Background(), []string{"/repo"}, false, out)
	if err != nil || got {
		t.Fatalf("clean+unchanged should skip: %v %v", got, err)
	}
}

func TestMarkApplied_WritesHead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("deadbeef\n")}, nil)
	w := &Workspace{runner: f}
	if err := w.markApplied(context.Background(), []string{"/repo"}); err != nil {
		t.Fatalf("markApplied: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(state, "zn-self-upgrade", "apply", "applied-hash", "repo"))
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if string(got) != "deadbeef\n" {
		t.Errorf("stored hash: got %q", got)
	}
}

func TestCheckNixDaemon_ErrorPath(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, &exec.CommandError{Name: "nix", Args: []string{"eval"}, Result: exec.Result{ExitCode: 1}})
	w := &Workspace{runner: f}
	if err := w.checkNixDaemon(context.Background()); err == nil {
		t.Fatal("expected daemon-check error")
	}
}
