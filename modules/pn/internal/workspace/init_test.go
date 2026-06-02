package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestInit_ClonesMissingRepos(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// Expect git clone for the missing repo.
	f.AddResponse("git", []string{"clone", "--branch", "main", "https://github.com/owner/foo.git", filepath.Join(root, "foo")}, exec.Result{}, nil)
	// rev-parse for lock generation.
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc1234\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// With no terminal configured, init writes an empty DAG lock (no revs).
	lock, err := ReadLock(filepath.Join(root, "pn-workspace.lock"))
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	if len(lock.Order) != 0 || len(lock.DependsOn) != 0 {
		t.Errorf("expected empty DAG lock, got order=%v dependsOn=%v", lock.Order, lock.DependsOn)
	}
}

func TestInit_SkipsExistingRepos(t *testing.T) {
	root := t.TempDir()
	// Create an existing "foo" repo dir to simulate already-cloned state.
	if err := os.MkdirAll(filepath.Join(root, "foo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// Only rev-parse should be called (no clone).
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("def5678\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	calls := f.Calls()
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "clone" {
			t.Errorf("did not expect git clone for existing repo, got call %v", c.Args)
		}
	}
}

func TestInit_ReconcileExistingClone(t *testing.T) {
	root := t.TempDir()
	// Existing "bar" clone NOT in TOML — should be added.
	if err := os.MkdirAll(filepath.Join(root, "bar", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	// remote get-url for bar (to populate URL field) — invoked first during reconcile
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "remote", "get-url", "origin"}, exec.Result{Stdout: []byte("https://github.com/owner/bar.git\n")}, nil)
	// Clone for foo
	f.AddResponse("git", []string{"clone", "--branch", "main", "https://github.com/owner/foo.git", filepath.Join(root, "foo")}, exec.Result{}, nil)
	// rev-parse for foo + bar (order alphabetical by name)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "bar"), "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("bbbbbbb\n")}, nil)
	f.AddResponse("git", []string{"-C", filepath.Join(root, "foo"), "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("aaaaaaa\n")}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Init(context.Background(), InitOptions{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// After init, TOML should mention bar.
	tomlData, err := os.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(tomlData, "bar") {
		t.Errorf("expected reconciled TOML to mention bar; got:\n%s", string(tomlData))
	}
}

func contains(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
