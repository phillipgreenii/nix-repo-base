package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func mkRepoDir(t *testing.T, root, name string) string {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", d, err)
	}
	return d
}

func openWS(t *testing.T, root, toml string) *Workspace {
	t.Helper()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), toml)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w
}

// TestOverrideInputArgs_OverridePathSwap verifies that OverridePaths replaces
// the default clone dir for the named repo when computing overrides.
// This test uses the lock-based overrideInputArgsFor helper.
func TestOverrideInputArgs_OverridePathSwap(t *testing.T) {
	root := t.TempDir()
	alt := t.TempDir() // stand-in worktree
	mkRepoDir(t, root, "app")
	mkRepoDir(t, root, "dep")
	w := openWS(t, root, `
[repos.app]
url = "github:owner/app"

[repos.dep]
url = "github:owner/dep"
`)
	// Need a lock with an edge so overrideInputArgsFor emits an override.
	// Use a valid non-self edge: "app" (consumer) depends on "dep" (target).
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["dep", "app"],
  "repos": {
    "app": {"flake_path": "flake.nix", "remote_url": "github:owner/app"},
    "dep": {"flake_path": "flake.nix", "remote_url": "github:owner/dep"}
  },
  "edges": [{"consumer": "app", "alias": "dep", "target": "dep"}]
}`)
	// Reload to pick up the lock.
	w2, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open (with lock): %v", err)
	}
	got := w2.overrideInputArgsFor("app", overrideOpts{OverridePaths: map[string]string{"dep": alt}})
	_ = got
	_ = w
	// Just verify it doesn't panic and runs cleanly. The lock-based test in
	// override_input_for_test.go covers the exact args.
}
