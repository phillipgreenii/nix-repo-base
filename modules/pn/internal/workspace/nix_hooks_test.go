package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestRepoNixRunString_InjectsConsumerOverrides verifies that a {nix_run}
// expansion for a consumer repo carries that repo's --override-input flags
// (from the effective lock) and an absolute flakeref, single-quoted.
func TestRepoNixRunString_InjectsConsumerOverrides(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.repo-base]\nurl=\"github:o/repo-base\"\n[repos.consumer]\nurl=\"github:o/consumer\"\n")
	for _, r := range []string{"repo-base", "consumer"} {
		mustMkdir(t, filepath.Join(root, r))
		writeFile(t, filepath.Join(root, r, "flake.nix"), "{}")
	}
	// A lock matching the config exactly ⇒ effectiveLock returns it without
	// deriving (no nix eval in unit tests).
	lk := &Lock{
		Repos: map[string]LockRepoEntry{
			"repo-base": {FlakePath: "flake.nix", RemoteURL: "github:o/repo-base"},
			"consumer":  {FlakePath: "flake.nix", RemoteURL: "github:o/consumer"},
		},
		Edges: []LockEdge{{Consumer: "consumer", Alias: "base", Target: "repo-base"}},
	}
	if err := WriteLock(filepath.Join(root, LockFileName), lk); err != nil {
		t.Fatal(err)
	}

	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.repoNixRunString(context.Background(), "consumer", "install-pre-commit-hooks")
	wantOverride := "--override-input base 'git+file://" + filepath.Join(root, "repo-base") + "'"
	if !strings.Contains(got, wantOverride) {
		t.Errorf("missing override in %q", got)
	}
	if !strings.HasSuffix(got, "'"+filepath.Join(root, "consumer")+"#install-pre-commit-hooks'") {
		t.Errorf("bad flakeref suffix in %q", got)
	}
}
