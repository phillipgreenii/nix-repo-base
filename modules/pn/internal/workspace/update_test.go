package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestUpdate_PullLocksPushPerRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	// dirty checks: both pass (clean).
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	// upstream check.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	// pull rebase autostash.
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	// update-locks.
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	// push.
	f.AddResponse("git", []string{"-C", foo, "push"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(f.Calls()) != 6 {
		t.Errorf("expected 6 calls, got %d (%+v)", len(f.Calls()), f.Calls())
	}
}

func TestUpdate_SkipsDirtyRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	// dirty check fails -> repo skipped.
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Only the dirty probe should be called; no pull/locks/push.
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "pull" || a == "push" {
				t.Errorf("expected no pull/push for dirty repo; got %v", c.Args)
			}
		}
	}
}

func TestUpdate_NoUpstreamRunsLocksOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	// no upstream
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	// update-locks still runs.
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), UpdateOptions{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "pull" || a == "push" {
				t.Errorf("expected no pull/push without upstream; got %v", c.Args)
			}
		}
	}
}
