package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// makeRepoWithFlakeAt creates a repo dir and writes a minimal flake.nix at the
// given relative path within the repo.
func makeRepoWithFlakeAt(t *testing.T, root, repoName, relFlakePath string) {
	t.Helper()
	absPath := filepath.Join(root, repoName, filepath.Dir(relFlakePath))
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", absPath, err)
	}
	writeFile(t, filepath.Join(root, repoName, relFlakePath), "{ outputs = {}; }")
}

// openWorkspaceWithLock opens a workspace and sets the lock in-memory.
func openWorkspaceWithLock(t *testing.T, root string, lock *Lock) *Workspace {
	t.Helper()
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.lock = lock
	return w
}

// TestResolveFlakePath_DefaultFlakeNix: flake at <repo>/flake.nix, no config FlakePath.
// resolveFlakePath should return "flake.nix" (default).
func TestResolveFlakePath_DefaultFlakeNix(t *testing.T) {
	root := t.TempDir()
	makeRepoWithFlakeAt(t, root, "myrepo", "flake.nix")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.resolveFlakePath("myrepo")
	if got != "flake.nix" {
		t.Errorf("resolveFlakePath = %q, want %q", got, "flake.nix")
	}
	// isDefaultFlakePath should report true.
	if !isDefaultFlakePath(got) {
		t.Errorf("isDefaultFlakePath(%q) = false, want true", got)
	}
}

// TestResolveFlakePath_SubdirNixFlakeNix: flake at <repo>/nix/flake.nix, no config.
// resolveFlakePath should return "nix/flake.nix" (second default).
func TestResolveFlakePath_SubdirNixFlakeNix(t *testing.T) {
	root := t.TempDir()
	makeRepoWithFlakeAt(t, root, "myrepo", "nix/flake.nix")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.resolveFlakePath("myrepo")
	if got != "nix/flake.nix" {
		t.Errorf("resolveFlakePath = %q, want %q", got, "nix/flake.nix")
	}
	if !isDefaultFlakePath(got) {
		t.Errorf("isDefaultFlakePath(%q) = false, want true", got)
	}
}

// TestResolveFlakePath_CustomPath: flake at non-default location, no config setting.
// resolveFlakePath returns "" (defaults don't match; non-default needs explicit config).
func TestResolveFlakePath_CustomPath(t *testing.T) {
	root := t.TempDir()
	makeRepoWithFlakeAt(t, root, "myrepo", "custom/dir/flake.nix")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.resolveFlakePath("myrepo")
	if got != "" {
		t.Errorf("resolveFlakePath without config = %q, want empty (non-default path not auto-discovered)", got)
	}
}

// TestResolveFlakePath_ExplicitConfig: config has flake_path = "custom/dir/flake.nix"
// and the file exists. resolveFlakePath should return the config value.
func TestResolveFlakePath_ExplicitConfig(t *testing.T) {
	root := t.TempDir()
	makeRepoWithFlakeAt(t, root, "myrepo", "custom/dir/flake.nix")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
flake_path = "custom/dir/flake.nix"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.resolveFlakePath("myrepo")
	if got != "custom/dir/flake.nix" {
		t.Errorf("resolveFlakePath with config = %q, want %q", got, "custom/dir/flake.nix")
	}
	if isDefaultFlakePath(got) {
		t.Errorf("isDefaultFlakePath(%q) = true, want false", got)
	}
}

// TestResolveFlakePath_NoFlake: no flake anywhere, no config setting.
// resolveFlakePath should return "".
func TestResolveFlakePath_NoFlake(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "myrepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)
	w, err := Open(root, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := w.resolveFlakePath("myrepo")
	if got != "" {
		t.Errorf("resolveFlakePath with no flake = %q, want empty", got)
	}
}

// TestResolveFlakePath_LockOverridesConfig: lock has FlakePath set, config does
// not. Lock value should win (priority 1 > 2).
func TestResolveFlakePath_LockOverridesConfig(t *testing.T) {
	root := t.TempDir()
	// Config has no flake_path; lock does.
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
url = "github:owner/myrepo"
`)
	lock := &Lock{
		Repos: map[string]LockRepoEntry{
			"myrepo": {FlakePath: "nix/flake.nix", RemoteURL: "github:owner/myrepo"},
		},
	}
	w := openWorkspaceWithLock(t, root, lock)
	got := w.resolveFlakePath("myrepo")
	if got != "nix/flake.nix" {
		t.Errorf("resolveFlakePath with lock = %q, want %q", got, "nix/flake.nix")
	}
}

// TestInit_DoesNotWriteDefaultFlakePath: when a repo's flake is at the default
// location (flake.nix), Init must NOT write flake_path to pn-workspace.toml.
func TestInit_DoesNotWriteDefaultFlakePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)
	// Repo already cloned with flake at default location.
	makeClonedRepo(t, root, "foo")
	makeRepoWithFlakeAt(t, root, "foo", "flake.nix")

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(t.Context(), &nopWriter{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Read back TOML and ensure flake_path is NOT written.
	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if contains(tomlData, "flake_path") {
		t.Errorf("flake_path must not be written for default location; TOML:\n%s", tomlData)
	}
}

// TestInit_WritesNonDefaultFlakePath: when a repo's flake is at a non-default
// location set in config, Init still sees it (config override path).
func TestInit_WritesNonDefaultFlakePath(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
flake_path = "nix/custom/flake.nix"
`)
	// Repo already cloned with flake at custom location.
	makeClonedRepo(t, root, "foo")
	makeRepoWithFlakeAt(t, root, "foo", "nix/custom/flake.nix")

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(t.Context(), &nopWriter{}, InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Read back TOML and ensure flake_path is preserved.
	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(tomlData, "flake_path") {
		t.Errorf("flake_path should be retained for non-default location; TOML:\n%s", tomlData)
	}
}

// TestReconcile_DoesNotWriteDefaultFlakePath: when reconcileFromFilesystem adds
// a new repo whose flake is at nix/flake.nix (second default), flake_path
// should NOT be written to the config.
func TestReconcile_DoesNotWriteDefaultFlakePath(t *testing.T) {
	root := t.TempDir()
	// "newrepo" exists on disk but not in TOML.
	makeClonedRepo(t, root, "newrepo")
	makeRepoWithFlakeAt(t, root, "newrepo", "nix/flake.nix")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.existing]
url = "github:owner/existing"
`)
	makeClonedRepo(t, root, "existing")

	f := exec.NewFakeRunner()
	// Expect git remote get-url for newrepo during reconcile.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "newrepo"), "remote", "get-url", "origin"},
		exec.Result{Stdout: []byte("https://github.com/owner/newrepo.git\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.reconcileFromFilesystem(t.Context()); err != nil {
		t.Fatalf("reconcileFromFilesystem: %v", err)
	}

	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}

	// newrepo should now be in TOML.
	if !contains(tomlData, "newrepo") {
		t.Errorf("expected newrepo in TOML; got:\n%s", tomlData)
	}
	// flake_path at nix/flake.nix is a default — must NOT be written.
	if contains(tomlData, "flake_path") {
		t.Errorf("flake_path for default location must not be written; TOML:\n%s", tomlData)
	}
}

// nopWriter is an io.Writer that discards all output.
type nopWriter struct{}

func (n *nopWriter) Write(p []byte) (int, error) { return len(p), nil }
