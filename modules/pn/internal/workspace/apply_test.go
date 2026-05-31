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
