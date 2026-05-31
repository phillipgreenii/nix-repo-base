package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestUpgrade_RunsUpdateThenApply(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	// Update sequence (clean, has upstream).
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{Stdout: []byte("origin/main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "pull", "--rebase", "--autostash"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "push"}, exec.Result{}, nil)
	// Apply sequence.
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	f.AddResponse("nix", []string{"build", "."}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Upgrade(context.Background(), UpgradeOptions{}); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	// Ensure both nix fmt + build were invoked, indicating Apply ran after Update.
	gotFmt := false
	gotBuild := false
	for _, c := range f.Calls() {
		if c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "fmt" {
			gotFmt = true
		}
		if c.Name == "nix" && len(c.Args) > 0 && c.Args[0] == "build" {
			gotBuild = true
		}
	}
	if !gotFmt || !gotBuild {
		t.Errorf("expected apply phase to run nix fmt + build; got calls=%+v", f.Calls())
	}
}
