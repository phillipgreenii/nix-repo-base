package workspace

import (
	"os"
	"path/filepath"
	"strings"
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
	// Write new-format lock.
	writeFile(t, filepath.Join(dir, LockFileName), `{
  "order": ["foo"],
  "repos": {"foo": {"remote_url": "github:owner/foo"}},
  "edges": []
}`)

	w, err := Open(dir, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if w.Config().Workspace.Name != "test" {
		t.Errorf("config name: %q", w.Config().Workspace.Name)
	}
	if len(w.Lock().Order) != 1 || w.Lock().Order[0] != "foo" {
		t.Errorf("lock order: %v", w.Lock().Order)
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
	if len(w.Lock().Order) != 0 || len(w.Lock().Repos) != 0 || len(w.Lock().Edges) != 0 {
		t.Errorf("expected empty lock, got order=%v repos=%v edges=%v", w.Lock().Order, w.Lock().Repos, w.Lock().Edges)
	}
}

// TestOpen_CorruptLockPropagatesError verifies that Open surfaces the
// ParseLock invariant error from ReadLock rather than silently loading a
// corrupt lock file.
func TestOpen_CorruptLockPropagatesError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pn-workspace.toml"), `
[workspace]
name = "test"

[repos.foo]
url = "github:owner/foo"
`)
	// Write a lock that violates invariant (e): terminal not in repos.
	writeFile(t, filepath.Join(dir, LockFileName), `{
  "terminal": "nonexistent",
  "order": ["foo"],
  "repos": {"foo": {"flake_path": "flake.nix", "remote_url": "github:owner/foo"}},
  "edges": []
}`)
	_, err := Open(dir, exec.NewFakeRunner())
	if err == nil {
		t.Fatal("expected error opening workspace with corrupt lock, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected terminal name in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pn workspace lock") {
		t.Errorf("expected regenerate hint in error, got: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// contains reports whether needle appears in haystack (byte-level substring
// search). Used by multiple test files that check TOML or error output.
func contains(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
