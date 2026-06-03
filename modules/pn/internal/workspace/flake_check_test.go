package workspace

import (
	"context"
	"path/filepath"
	"reflect"
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

// TestFlakeCheck_InjectsLocalOverrides verifies that each repo is checked with
// --override-input flags pinning its OTHER local workspace siblings — excluding
// the terminal (the build target) and the repo under test (the flake itself).
// This mirrors the bash, which ran each check via pn-ws-nix.
func TestFlakeCheck_InjectsLocalOverrides(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"term", "base", "overlay"} {
		mkRepoDir(t, root, r)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "term"

[repos.term]
url = "github:o/term"

[repos.base]
url = "github:o/base"

[repos.overlay]
url = "github:o/overlay"
`)
	base := filepath.Join(root, "base")
	overlay := filepath.Join(root, "overlay")

	// Iteration is alphabetical: base, overlay, term.
	// base    -> override overlay (exclude terminal=term and self=base)
	// overlay -> override base
	// term    -> override base + overlay (terminal excludes only itself)
	wantArgs := [][]string{
		{"flake", "check", "--override-input", "overlay", "git+file://" + overlay},
		{"flake", "check", "--override-input", "base", "git+file://" + base},
		{"flake", "check",
			"--override-input", "base", "git+file://" + base,
			"--override-input", "overlay", "git+file://" + overlay},
	}

	f := exec.NewFakeRunner()
	for _, a := range wantArgs {
		f.AddResponse("nix", a, exec.Result{}, nil)
	}

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.FlakeCheck(context.Background(), FlakeCheckOptions{}); err != nil {
		t.Fatalf("FlakeCheck: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	wantDirs := []string{base, overlay, filepath.Join(root, "term")}
	for i, c := range calls {
		if !reflect.DeepEqual(c.Args, wantArgs[i]) {
			t.Errorf("call %d args:\n got %#v\nwant %#v", i, c.Args, wantArgs[i])
		}
		if c.Opts.Dir != wantDirs[i] {
			t.Errorf("call %d dir: got %q want %q", i, c.Opts.Dir, wantDirs[i])
		}
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
