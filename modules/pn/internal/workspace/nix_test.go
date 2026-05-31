package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestNixCommand_AppendsOverrideInputForEachLockedRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	writeFile(t, filepath.Join(root, "pn-workspace.lock"), `{"repos":{"foo":{"url":"github:owner/foo","rev":"abc"}}}`)

	f := exec.NewFakeRunner()
	expected := []string{
		"flake", "check",
		"--override-input", "foo", "path:" + filepath.Join(root, "foo"),
	}
	f.AddResponse("nix", expected, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.NixCommand(context.Background(), []string{"flake", "check"}); err != nil {
		t.Fatalf("NixCommand: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Opts.Dir != root {
		t.Errorf("expected dir=%s, got %q", root, calls[0].Opts.Dir)
	}
}

func TestNixCommand_NoLockedRepos(t *testing.T) {
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
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, "pn-workspace.lock"), `{"repos":{"foo":{"url":"github:owner/foo","rev":"f"},"bar":{"url":"github:owner/bar","rev":"b"}}}`)

	f := exec.NewFakeRunner()
	// bar < foo alphabetically -> bar override comes first.
	expected := []string{
		"build", ".",
		"--override-input", "bar", "path:" + filepath.Join(root, "bar"),
		"--override-input", "foo", "path:" + filepath.Join(root, "foo"),
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
