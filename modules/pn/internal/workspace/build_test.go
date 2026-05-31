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

func TestBuild_InjectsOverrideInputForLockedRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	// Both repos locked => both should appear as --override-input flags
	// on the nix build command, in alphabetical order (bar < foo).
	writeFile(t, filepath.Join(root, "pn-workspace.lock"), `{"repos":{"foo":{"url":"github:owner/foo","rev":"f"},"bar":{"url":"github:owner/bar","rev":"b"}}}`)

	f := exec.NewFakeRunner()
	barDir := filepath.Join(root, "bar")
	fooDir := filepath.Join(root, "foo")
	overrideArgs := []string{
		"build",
		"--override-input", "bar", "path:" + barDir,
		"--override-input", "foo", "path:" + fooDir,
		".",
	}
	// Per-repo: fmt (no overrides), then build with overrides.
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // bar fmt
	f.AddResponse("nix", overrideArgs, exec.Result{}, nil)    // bar build
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // foo fmt
	f.AddResponse("nix", overrideArgs, exec.Result{}, nil)    // foo build

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Build(context.Background(), BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(calls))
	}
	// Build calls (indices 1, 3) must carry override flags.
	for _, idx := range []int{1, 3} {
		args := calls[idx].Args
		if len(args) != len(overrideArgs) {
			t.Errorf("call %d: expected %d args, got %d (%v)", idx, len(overrideArgs), len(args), args)
			continue
		}
		for i, want := range overrideArgs {
			if args[i] != want {
				t.Errorf("call %d arg[%d]: %q, want %q", idx, i, args[i], want)
			}
		}
	}
}
