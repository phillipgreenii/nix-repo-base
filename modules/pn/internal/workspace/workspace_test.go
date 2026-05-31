package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestOpen_LoadsConfigAndLock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pn-workspace.toml"), `
[workspace]
name = "test"

[repos.foo]
url = "github:owner/foo"
`)
	writeFile(t, filepath.Join(dir, "pn-workspace.lock"), `{"repos":{"foo":{"url":"github:owner/foo","rev":"abc"}}}`)

	w, err := Open(dir, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if w.Config().Workspace.Name != "test" {
		t.Errorf("config name: %q", w.Config().Workspace.Name)
	}
	if w.Lock().Repos["foo"].Rev != "abc" {
		t.Errorf("lock rev: %q", w.Lock().Repos["foo"].Rev)
	}
	if w.Root() != dir {
		t.Errorf("root: %q want %q", w.Root(), dir)
	}
}

func TestOpen_MissingTOML(t *testing.T) {
	dir := t.TempDir()
	_, err := Open(dir, exec.NewFakeRunner())
	if err == nil {
		t.Fatal("expected error opening workspace without pn-workspace.toml")
	}
}

func TestOpen_MissingLockIsTolerated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pn-workspace.toml"), `[workspace]
name = "x"
`)
	w, err := Open(dir, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open should tolerate missing lock, got %v", err)
	}
	if len(w.Lock().Repos) != 0 {
		t.Errorf("expected empty lock, got %d entries", len(w.Lock().Repos))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
