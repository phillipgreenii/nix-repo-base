// internal/workspace/hookbundle_notenrolled_test.go
package workspace

import (
	"path/filepath"
	"testing"
)

// hbNoFlakeRepo is a real git repo with no flake.nix anywhere.
func hbNoFlakeRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	initRealRepo(t, dir)
	return dir
}

func wantHooks(t *testing.T, dir string, state HookBundleState, hooks string) HookBundleInfo {
	t.Helper()
	info := wantState(t, dir, state)
	if info.Hooks != hooks {
		t.Fatalf("hooks = %q, want %q (state %s, info %+v)", info.Hooks, hooks, state, info)
	}
	return info
}
