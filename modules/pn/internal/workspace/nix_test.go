package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNixCommand_AppendsGitFileOverrideForEachRepo(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_NoRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "x"
`)
	f := exec.NewFakeRunner()
	f.AddResponse("nix", []string{"flake", "show"}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "show"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_MultipleOverridesAlphabetical(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "foo")
	mkRepoDir(t, root, "bar")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"build", ".",
		"--override-input", "bar", "git+file://" + filepath.Join(root, "bar"),
		"--override-input", "foo", "git+file://" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"build", "."}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}

func TestNixCommand_UsesConfiguredInputName(t *testing.T) {
	root := t.TempDir()
	mkRepoDir(t, root, "phillipg-nix-repo-base")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.phillipg-nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
input-name = "phillipgreenii-nix-base"
`)
	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "phillipgreenii-nix-base", "git+file://" + filepath.Join(root, "phillipg-nix-repo-base"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
}
