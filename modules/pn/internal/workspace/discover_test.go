package workspace

import (
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestDiscover_ReturnsReposAlphabetical(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repos := w.Discover()
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].Name != "bar" {
		t.Errorf("expected bar first; got %q", repos[0].Name)
	}
	if repos[0].Path != filepath.Join(root, "bar") {
		t.Errorf("bar path: got %q", repos[0].Path)
	}
	if repos[0].URL != "github:owner/bar" {
		t.Errorf("bar url: got %q", repos[0].URL)
	}
	if repos[1].Name != "foo" {
		t.Errorf("expected foo second; got %q", repos[1].Name)
	}
}

func TestDiscover_EmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `[workspace]
name = "empty"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(w.Discover()) != 0 {
		t.Errorf("expected empty repo list")
	}
}
