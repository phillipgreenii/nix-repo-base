package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceRoot_WalkUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := resolveWorkspaceRoot("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	gotR, _ := filepath.EvalSymlinks(got)
	rootR, _ := filepath.EvalSymlinks(root)
	if gotR != rootR {
		t.Errorf("got %q want %q", gotR, rootR)
	}
}

func TestResolveWorkspaceRoot_FlagMissingToml(t *testing.T) {
	if _, err := resolveWorkspaceRoot(t.TempDir()); err == nil {
		t.Fatal("expected error when --root has no pn-workspace.toml")
	}
}
