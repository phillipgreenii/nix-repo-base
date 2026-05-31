package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestFlakeCheck_AllPass(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.FlakeCheck(context.Background(), FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("first dir: got %q", calls[0].Opts.Dir)
	}
	if calls[1].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("second dir: got %q", calls[1].Opts.Dir)
	}
}

func TestFlakeCheck_ContinuesPastFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	// bar fails, foo passes — but BOTH must run.
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("nix", []string{"flake", "check"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.FlakeCheck(context.Background(), FlakeCheckOptions{})
	if err == nil {
		t.Fatal("expected error reporting failures, got nil")
	}
	if !strings.Contains(err.Error(), "bar") {
		t.Errorf("error should name the failing repo (bar); got %q", err.Error())
	}
	if len(f.Calls()) != 2 {
		t.Errorf("expected both repos to be attempted; got %d calls", len(f.Calls()))
	}
}
