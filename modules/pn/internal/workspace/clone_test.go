package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// makeClonedRepo creates a fake cloned repo dir (with .git) so isGitRepo returns true.
func makeClonedRepo(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
		t.Fatalf("makeClonedRepo %s: %v", name, err)
	}
}

// TestClone_HappyPath verifies that Clone clones missing repos and skips
// existing ones. With a 3-repo config and 1 already cloned, only 2 clones run.
func TestClone_HappyPath(t *testing.T) {
	root := t.TempDir()
	// "existing" is already cloned.
	makeClonedRepo(t, root, "existing")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.existing]
url = "github:owner/existing"

[repos.missing-a]
url = "github:owner/missing-a"

[repos.missing-b]
url = "github:owner/missing-b"
`)

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{
		"clone", "--branch", "main",
		"https://github.com/owner/missing-a.git", filepath.Join(root, "missing-a"),
	},
		exec.Result{}, nil)
	f.AddResponse("git", []string{
		"clone", "--branch", "main",
		"https://github.com/owner/missing-b.git", filepath.Join(root, "missing-b"),
	},
		exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Clone(context.Background(), &out, CloneOptions{}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Exactly 2 clone calls (alphabetical order: missing-a, missing-b).
	calls := f.Calls()
	var cloneCalls []exec.Call
	for _, c := range calls {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "clone" {
			cloneCalls = append(cloneCalls, c)
		}
	}
	if len(cloneCalls) != 2 {
		t.Errorf("expected 2 clone calls, got %d", len(cloneCalls))
	}
	// Verify streaming output.
	for _, c := range cloneCalls {
		if c.Opts.Stdout == nil {
			t.Errorf("clone should stream output (Opts.Stdout set); args=%v", c.Args)
		}
	}
}

// TestClone_Idempotent verifies that running Clone twice produces no git calls
// on the second run when all repos are already present.
func TestClone_Idempotent(t *testing.T) {
	root := t.TempDir()
	makeClonedRepo(t, root, "foo")
	makeClonedRepo(t, root, "bar")

	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// First run — no repos to clone.
	if err := w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{}); err != nil {
		t.Fatalf("Clone first run: %v", err)
	}
	// Second run — still no repos to clone.
	if err := w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{}); err != nil {
		t.Fatalf("Clone second run: %v", err)
	}

	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "clone" {
			t.Errorf("unexpected git clone call: %v", c.Args)
		}
	}
}

// TestClone_FailurePropagates verifies that a git clone failure returns an
// error naming the failing repo.
func TestClone_FailurePropagates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	cloneErr := &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}}
	f.AddResponse("git", []string{
		"clone", "--branch", "main",
		"https://github.com/owner/foo.git", filepath.Join(root, "foo"),
	},
		exec.Result{ExitCode: 128}, cloneErr)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{})
	if err == nil {
		t.Fatal("expected error on clone failure, got nil")
	}
	// Error should name the failing repo.
	if !contains([]byte(err.Error()), "foo") {
		t.Errorf("error should name failing repo 'foo'; got: %v", err)
	}
}

// TestClone_MultiRemote verifies that a repo with [[repos.X.remotes]] entries
// gets each non-origin remote added after cloning from origin.
func TestClone_MultiRemote(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.myrepo]
branch = "main"

[[repos.myrepo.remotes]]
name = "origin"
url = "https://origin.host/owner/myrepo.git"

[[repos.myrepo.remotes]]
name = "fork"
url = "https://fork.host/me/myrepo.git"

[[repos.myrepo.remotes]]
name = "upstream"
url = "https://upstream.host/upstream/myrepo.git"
`)

	repoDir := filepath.Join(root, "myrepo")

	f := exec.NewFakeRunner()
	// Clone from origin.
	f.AddResponse("git", []string{
		"clone", "--branch", "main",
		"https://origin.host/owner/myrepo.git", repoDir,
	},
		exec.Result{}, nil)
	// Add fork remote.
	f.AddResponse("git", []string{
		"-C", repoDir, "remote", "add", "fork",
		"https://fork.host/me/myrepo.git",
	},
		exec.Result{}, nil)
	// Add upstream remote.
	f.AddResponse("git", []string{
		"-C", repoDir, "remote", "add", "upstream",
		"https://upstream.host/upstream/myrepo.git",
	},
		exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	calls := f.Calls()
	if len(calls) != 3 {
		t.Errorf("expected 3 git calls (clone + 2 remote add), got %d: %v", len(calls), calls)
	}
}

// TestClone_TerminalFlagAccepted verifies the --terminal flag is accepted
// without error (it has no behavioral effect on clone).
func TestClone_TerminalFlagAccepted(t *testing.T) {
	root := t.TempDir()
	makeClonedRepo(t, root, "foo")
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{Terminal: "foo"}); err != nil {
		t.Fatalf("Clone with Terminal option: %v", err)
	}
}
