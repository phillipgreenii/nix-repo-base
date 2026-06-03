package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const applyTOML = `
[workspace]
terminal = "leaf"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
input-name = "dep-input"
`

func TestApply_RunsApplyCommandWithOverrides(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	leafDir := filepath.Join(root, "leaf")
	depDir := filepath.Join(root, "dep")

	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"eval", "--expr", "true"}, exec.Result{}, nil) // daemon check
	f.AddResponse("nix", []string{"fmt"}, exec.Result{}, nil)
	f.AddResponse("sudo", []string{
		"darwin-rebuild", "switch", "--flake", leafDir + "#" + shortHostname(),
		"--override-input", "dep-input", "git+file://" + depDir,
	}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", depDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("d\n")}, nil)
	f.AddResponse("git", []string{"-C", leafDir, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("l\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{Force: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Output names the terminal project.
	if !strings.Contains(out.String(), "leaf") {
		t.Errorf("apply output should name the terminal project %q; got:\n%s", "leaf", out.String())
	}
	// The apply command streams its output live (Opts.Stdout set).
	var streamed bool
	for _, c := range f.Calls() {
		if c.Name == "sudo" {
			streamed = c.Opts.Stdout != nil
		}
	}
	if !streamed {
		t.Errorf("apply command should stream output (Opts.Stdout set)")
	}
}

func TestApply_ErrorsWhenApplyCommandMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Apply(context.Background(), &bytes.Buffer{}, ApplyOptions{}); err == nil {
		t.Fatal("expected error when apply_command unset")
	}
}

func TestApply_ShowNixCommandsOnly(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf")
	mkRepoDir(t, root, "dep")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), applyTOML)
	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out := &bytes.Buffer{}
	if err := w.Apply(context.Background(), out, ApplyOptions{ShowNixCommandsOnly: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("dry-run must not run anything; got %d calls", len(f.Calls()))
	}
	if !strings.Contains(out.String(), "sudo darwin-rebuild switch --flake "+filepath.Join(root, "leaf")) {
		t.Errorf("dry-run output missing apply command:\n%s", out.String())
	}
}

func TestAllRepoDirs_SkipsMissingClones(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "leaf") // cloned
	// "dep" declared but NOT cloned on disk.
	w := openWS(t, root, `
[workspace]
terminal = "leaf"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`)
	got := w.allRepoDirs(nil)
	want := []string{filepath.Join(root, "leaf")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allRepoDirs should skip missing clones: got %#v want %#v", got, want)
	}
}
