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
	got, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: "/x", gitDir: "/x"}}, true, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("force should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_DirtyTree(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", "/repo", "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte(" M file\n")}, nil)
	w := &Workspace{runner: f}
	got, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: "/repo", gitDir: "/repo"}}, false, &bytes.Buffer{})
	if err != nil || !got {
		t.Fatalf("dirty tree should rebuild: %v %v", got, err)
	}
}

func TestNeedsRebuild_ReadsNewStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const dir = "/repo" // a fake repo dir; the runner is faked, so it need not exist
	f := exec.NewFakeRunner()
	// clean working tree; HEAD == the applied_ref we seed below
	f.AddResponse("git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc123\n")}, nil)
	w := &Workspace{runner: f}
	// seed the new store so HEAD matches -> should SKIP
	if err := writeAppliedState(dir, AppliedState{AppliedRef: "abc123"}); err != nil {
		t.Fatal(err)
	}
	rebuild, err := w.needsRebuild(context.Background(), []repoDir{{keyPath: dir, gitDir: dir}}, false, &bytes.Buffer{})
	if err != nil || rebuild {
		t.Fatalf("clean + matching applied_ref should skip rebuild; rebuild=%v err=%v", rebuild, err)
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

func TestMarkApplied_WriteFailIsReturned(t *testing.T) {
	// Point XDG_DATA_HOME at a regular file (not a dir) so writeAppliedState's
	// MkdirAll under it fails.
	bad := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", bad)
	const dir = "/repo"
	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", dir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc\n")}, nil)
	f.AddResponse("git", []string{"-C", dir, "-c", "core.fsmonitor=false", "status", "--porcelain"}, exec.Result{Stdout: []byte("")}, nil)
	w := &Workspace{runner: f}
	if err := w.markApplied(context.Background(), []repoDir{{keyPath: dir, gitDir: dir}}); err == nil {
		t.Fatal("markApplied must return the store-write error (fail-closed)")
	}
}
