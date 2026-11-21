// internal/workspace/hookbundle_gate_test.go
package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// gateWS opens a workspace whose repo "a" is a real git repo with a post-clone
// {nix_run install-pre-commit-hooks} hook and a tracked flake.lock/flake.nix.
// Nothing scripts an `sh` response: a hook that is NOT skipped shows up in
// shCalls (and the unscripted call is warned, post-phase, not fatal).
func gateWS(t *testing.T) (*Workspace, string) {
	t.Helper()
	lk := &Lock{Repos: map[string]LockRepoEntry{"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"}}}
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-clone\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n",
		[]string{"a"}, lk)
	dir := filepath.Join(w.root, "a")
	initRealRepo(t, dir)
	writeFile(t, filepath.Join(dir, "flake.lock"), "{}\n")
	writeFile(t, filepath.Join(dir, "flake.nix"), "{}\n")
	runGitT(t, dir, "add", "flake.lock", "flake.nix")
	runGitT(t, dir, "commit", "-q", "-m", "flake")
	return w, dir
}

// fireInstall runs the post-clone event for repo a and returns the sh calls that
// THIS firing made (the fake runner's record is cumulative).
func fireInstall(t *testing.T, w *Workspace) []exec.Call {
	t.Helper()
	f := w.runner.(*exec.FakeRunner)
	before := len(shCalls(f))
	var out, errOut bytes.Buffer
	if err := w.RunEventHooks(context.Background(), HookPhasePost, "clone", []string{"a"}, &out, &errOut); err != nil {
		t.Fatalf("RunEventHooks: %v", err)
	}
	return shCalls(f)[before:]
}

// existingNixStoreFile returns a regular file under /nix/store (an old
// generated-config symlink resolved to a file), or "" when there is none.
func existingNixStoreFile(t *testing.T) string {
	t.Helper()
	// Read /nix/store in small batches and stop at the first regular entry
	// (a .drv or a source file): globbing every store path's children takes
	// seconds on a populated store.
	d, err := os.Open("/nix/store")
	if err != nil {
		return ""
	}
	defer func() { _ = d.Close() }()
	for {
		ents, err := d.ReadDir(256)
		for _, e := range ents {
			if e.Type().IsRegular() {
				return filepath.Join("/nix/store", e.Name())
			}
		}
		if err != nil {
			return ""
		}
	}
}

// setWS opens a workspace whose root plays a workforest set: repo "a" is a
// LINKED worktree of a canonical clone, and repo "b" is a producer a consumes
// (lock edge alias base -> b), so the set's pins are non-empty.
func setWS(t *testing.T) (w *Workspace, canonical, setA string) {
	t.Helper()
	lk := &Lock{
		Repos: map[string]LockRepoEntry{
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
			"b": {FlakePath: "flake.nix", RemoteURL: "github:o/b"},
		},
		Edges: []LockEdge{{Consumer: "a", Alias: "base", Target: "b"}},
	}
	w = openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-clone\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n[repos.b]\nurl=\"github:o/b\"\n",
		[]string{"b"}, lk)
	canonical = hbRepo(t)
	setA = filepath.Join(w.root, "a")
	runGitT(t, canonical, "worktree", "add", "-q", "-b", "feat", setA)
	return w, canonical, setA
}
