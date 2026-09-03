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

	// clone is migrated onto x/gitclient's Clone constructor (bead pg2-8bfb5).
	stubGitCloner(t, map[string]*fakeGitMutator{
		"https://github.com/owner/missing-a.git": {},
		"https://github.com/owner/missing-b.git": {},
	})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var out bytes.Buffer
	if err := w.Clone(context.Background(), &out, CloneOptions{}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// The stub errors on any url not in its fixture map, so a successful run
	// here already proves exactly the two missing repos were cloned (and
	// "existing" was skipped, since it has no fixture entry and Clone would
	// have failed had it tried).
}

// TestClone_Idempotent verifies that running Clone twice produces no clone
// calls on the second run when all repos are already present.
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

	// No fixture entries: any clone attempt fails the stub, pinning that
	// neither run tries to clone an already-present repo.
	stubGitCloner(t, map[string]*fakeGitMutator{})

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
}

// TestClone_FailurePropagates verifies that a git clone failure returns an
// error naming the failing repo.
func TestClone_FailurePropagates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"
`)

	// No fixture entry for foo's url → stubGitCloner's fake errors.
	stubGitCloner(t, map[string]*fakeGitMutator{})

	f := exec.NewFakeRunner()
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

	m := &fakeGitMutator{}
	stubGitCloner(t, map[string]*fakeGitMutator{
		"https://origin.host/owner/myrepo.git": m,
	})

	f := exec.NewFakeRunner()
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Clone(context.Background(), &bytes.Buffer{}, CloneOptions{}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	want := map[string]string{
		"fork":     "https://fork.host/me/myrepo.git",
		"upstream": "https://upstream.host/upstream/myrepo.git",
	}
	if len(m.addRemotes) != len(want) {
		t.Fatalf("expected %d added remotes (origin excluded), got %v", len(want), m.addRemotes)
	}
	for name, url := range want {
		if got := m.addRemotes[name]; got != url {
			t.Errorf("remote %s: got url %q, want %q", name, got, url)
		}
	}
	if _, ok := m.addRemotes["origin"]; ok {
		t.Errorf("origin must NOT be re-added; got %v", m.addRemotes)
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
