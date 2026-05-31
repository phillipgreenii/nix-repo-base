package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestApply_PerRepoInOrder(t *testing.T) {
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
	if err := w.Apply(context.Background(), ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(calls))
	}
	if calls[0].Opts.Dir != filepath.Join(root, "bar") {
		t.Errorf("first repo should be bar, got dir %q", calls[0].Opts.Dir)
	}
	if calls[2].Opts.Dir != filepath.Join(root, "foo") {
		t.Errorf("second repo should be foo, got dir %q", calls[2].Opts.Dir)
	}
}

func TestApply_InjectsOverrideInputForLockedRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	// Both repos locked => both must appear as --override-input flags
	// on every rebuild invocation, in alphabetical order (bar < foo).
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
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // bar fmt
	f.AddResponse("nix", overrideArgs, exec.Result{}, nil)    // bar build (apply)
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil) // foo fmt
	f.AddResponse("nix", overrideArgs, exec.Result{}, nil)    // foo build (apply)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls, got %d", len(calls))
	}
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
