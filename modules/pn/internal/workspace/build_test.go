package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestBuild_FmtAndBuildPerRepoInOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// Alphabetical: bar then foo.
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"build", "."}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"build", "."}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls (2 repos x fmt+build), got %d", len(calls))
	}
	wantDirs := []string{
		filepath.Join(root, "bar"),
		filepath.Join(root, "bar"),
		filepath.Join(root, "foo"),
		filepath.Join(root, "foo"),
	}
	for i, c := range calls {
		if c.Opts.Dir != wantDirs[i] {
			t.Errorf("call %d: dir %q, want %q", i, c.Opts.Dir, wantDirs[i])
		}
	}
	if calls[0].Args[0] != "fmt" || calls[1].Args[0] != "build" {
		t.Errorf("expected fmt then build for first repo; got %v / %v", calls[0].Args, calls[1].Args)
	}
}

func TestBuild_AbortsOnError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"fmt"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), BuildOptions{}); err == nil {
		t.Fatal("expected error from nix fmt failure; got nil")
	}
}
