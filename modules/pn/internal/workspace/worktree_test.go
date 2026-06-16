package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ---- helpers ----

// makeTwoRepoWorkspace sets up a temp workspace with two repos (bar, foo) and
// a fake runner. Returns the root, setDir, and the fake runner.
func makeTwoRepoWorkspace(t *testing.T) (root string, f *exec.FakeRunner) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["bar","foo"],
  "repos": {"foo": {"remote_url": "github:owner/foo"}, "bar": {"remote_url": "github:owner/bar"}},
  "edges": []
}`)
	f = exec.NewFakeRunner()
	return root, f
}

// makeFakeCanonicalRepos creates .git dirs for each repo under root so
// isGitRepo() returns true.
func makeFakeCanonicalRepos(t *testing.T, root string, repos ...string) {
	t.Helper()
	for _, repo := range repos {
		if err := os.MkdirAll(filepath.Join(root, repo, ".git"), 0o755); err != nil {
			t.Fatalf("makeFakeCanonicalRepos %s: %v", repo, err)
		}
	}
}

// addWorktreeListResponse scripts a `git worktree list --porcelain` response
// that does NOT contain the given branch (clean: branch not checked out).
func addWorktreeListClean(f *exec.FakeRunner, canonical, repo string) {
	f.AddResponse("git", []string{"-C", canonical, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte("worktree /some/path\nHEAD abc123\nbranch refs/heads/main\n\n")}, nil)
	_ = repo // just for readability at call sites
}

// addWorktreeListWithBranch scripts a response where <branch> IS checked out.
func addWorktreeListWithBranch(f *exec.FakeRunner, canonical, branch string) {
	output := "worktree /some/path\nHEAD abc123\nbranch refs/heads/" + branch + "\n\n"
	f.AddResponse("git", []string{"-C", canonical, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte(output)}, nil)
}

// addBranchNotExists scripts rev-parse --verify to fail (branch doesn't exist).
func addBranchNotExists(f *exec.FakeRunner, canonical, branch string) {
	f.AddResponse("git",
		[]string{"-C", canonical, "rev-parse", "--verify", "--quiet", "refs/heads/" + branch},
		exec.Result{ExitCode: 128},
		&exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}},
	)
}

// addBranchExists scripts rev-parse --verify to succeed (branch exists locally).
func addBranchExists(f *exec.FakeRunner, canonical, branch string) {
	f.AddResponse("git",
		[]string{"-C", canonical, "rev-parse", "--verify", "--quiet", "refs/heads/" + branch},
		exec.Result{Stdout: []byte("abc123\n")}, nil)
}

// ============================================================
// WorktreeAdd — pre-flight: missing canonical repo
// ============================================================

func TestWorktreeAdd_PreflightMissingRepo(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	// Only create canonical dir for "bar"; "foo" is missing.
	makeFakeCanonicalRepos(t, root, "bar")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var out bytes.Buffer
	err = w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"})
	if err == nil {
		t.Fatal("expected error for missing canonical repo, got nil")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error should name missing repo 'foo'; got: %v", err)
	}
	// Set dir must NOT have been created.
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	if dirExists(setDir) {
		t.Errorf("set dir should not be created on preflight failure; found %s", setDir)
	}
}

// ============================================================
// WorktreeAdd — pre-flight: set dir already exists
// ============================================================

func TestWorktreeAdd_PreflightSetDirExists(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Pre-create the set dir.
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var out bytes.Buffer
	err = w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"})
	if err == nil {
		t.Fatal("expected error when set dir already exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists'; got: %v", err)
	}
	// No git calls should have been made (pre-flight aborted before worktree list).
	for _, c := range f.Calls() {
		t.Errorf("unexpected git call: %v", c.Args)
	}
}

// ============================================================
// WorktreeAdd — pre-flight: branch already checked out
// ============================================================

func TestWorktreeAdd_PreflightBranchCheckedOut(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// bar: branch NOT checked out (clean)
	addWorktreeListClean(f, filepath.Join(root, "bar"), "bar")
	// foo: branch IS already checked out
	addWorktreeListWithBranch(f, filepath.Join(root, "foo"), "feature")

	var out bytes.Buffer
	err = w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"})
	if err == nil {
		t.Fatal("expected error when branch already checked out, got nil")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error should name repo 'foo'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "feature") {
		t.Errorf("error should name branch 'feature'; got: %v", err)
	}
	// Set dir must NOT have been created.
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	if dirExists(setDir) {
		t.Errorf("set dir should not be created on preflight failure; found %s", setDir)
	}
}

// ============================================================
// WorktreeAdd — happy path: new branch (no prior local branch)
// ============================================================

func TestWorktreeAdd_HappyPath_NewBranch(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	// Write config files so copy succeeds.
	writeFile(t, filepath.Join(root, ConfigFileName), `[repos.foo]
url = "github:owner/foo"
[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":[],"repos":{},"edges":[]}`)
	writeFile(t, filepath.Join(root, RevLockFileName), `{}`)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")

	// Pre-flight: worktree list (branch not checked out anywhere).
	addWorktreeListClean(f, barCanonical, "bar")
	addWorktreeListClean(f, fooCanonical, "foo")
	// Branch does not exist locally in either repo.
	addBranchNotExists(f, barCanonical, "feature")
	addBranchNotExists(f, fooCanonical, "feature")
	// git worktree add -b feature {setRepo} (no commit-ish).
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "add", "-b", "feature", fooSet}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	// Verify git args: should use -b form without commit-ish.
	calls := f.Calls()
	var addCalls []exec.Call
	for _, c := range calls {
		if len(c.Args) > 3 && c.Args[3] == "add" {
			addCalls = append(addCalls, c)
		}
	}
	if len(addCalls) != 2 {
		t.Fatalf("expected 2 worktree add calls, got %d", len(addCalls))
	}
	for _, c := range addCalls {
		// Should contain "-b" and "feature" but NOT a commit-ish.
		hasDashB := false
		for _, a := range c.Args {
			if a == "-b" {
				hasDashB = true
			}
		}
		if !hasDashB {
			t.Errorf("new-branch add should use -b flag; args=%v", c.Args)
		}
		// Should be exactly 6 args: git -C canonical worktree add -b feature setRepo
		// (no commit-ish appended)
		if len(c.Args) != 7 {
			t.Errorf("new-branch add (no commit-ish) should have 7 args; got %d: %v", len(c.Args), c.Args)
		}
	}

	// Config files should be copied.
	if !fileExists(filepath.Join(setDir, ConfigFileName)) {
		t.Errorf("ConfigFileName not copied to set dir")
	}
	if !fileExists(filepath.Join(setDir, LockFileName)) {
		t.Errorf("LockFileName not copied to set dir")
	}
	if !fileExists(filepath.Join(setDir, RevLockFileName)) {
		t.Errorf("RevLockFileName not copied to set dir (was present in canonical)")
	}
}

// ============================================================
// WorktreeAdd — new branch with explicit commit-ish
// ============================================================

func TestWorktreeAdd_HappyPath_NewBranchWithCommitIsh(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	writeFile(t, filepath.Join(root, ConfigFileName), `[repos.foo]
url = "github:owner/foo"
[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":[],"repos":{},"edges":[]}`)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")

	addWorktreeListClean(f, barCanonical, "bar")
	addWorktreeListClean(f, fooCanonical, "foo")
	addBranchNotExists(f, barCanonical, "feature")
	addBranchNotExists(f, fooCanonical, "feature")
	// With commit-ish: args end in the commit-ish.
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet, "abc1234"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "add", "-b", "feature", fooSet, "abc1234"}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature", CommitIsh: "abc1234"}); err != nil {
		t.Fatalf("WorktreeAdd with commit-ish: %v", err)
	}

	// Verify commit-ish appended.
	for _, c := range f.Calls() {
		if len(c.Args) > 3 && c.Args[3] == "add" && contains([]byte(strings.Join(c.Args, " ")), "-b") {
			last := c.Args[len(c.Args)-1]
			if last != "abc1234" {
				t.Errorf("expected commit-ish 'abc1234' as last arg; got %q in %v", last, c.Args)
			}
		}
	}
}

// ============================================================
// WorktreeAdd — happy path: branch already exists locally
// ============================================================

func TestWorktreeAdd_HappyPath_ExistingBranch(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	writeFile(t, filepath.Join(root, ConfigFileName), `[repos.foo]
url = "github:owner/foo"
[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":[],"repos":{},"edges":[]}`)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")

	addWorktreeListClean(f, barCanonical, "bar")
	addWorktreeListClean(f, fooCanonical, "foo")
	// Both repos have branch locally.
	addBranchExists(f, barCanonical, "feature")
	addBranchExists(f, fooCanonical, "feature")
	// Check-out form: no -b.
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", barSet, "feature"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "add", fooSet, "feature"}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorktreeAdd existing branch: %v", err)
	}

	// Verify no -b flag.
	for _, c := range f.Calls() {
		if len(c.Args) > 3 && c.Args[3] == "add" {
			for _, a := range c.Args {
				if a == "-b" {
					t.Errorf("existing-branch checkout must not use -b; args=%v", c.Args)
				}
			}
		}
	}
}

// ============================================================
// WorktreeRemove — happy path
// ============================================================

func TestWorktreeRemove_HappyPath(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create a fake set dir with repo subdirs.
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")
	if err := os.MkdirAll(barSet, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fooSet, 0o755); err != nil {
		t.Fatal(err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")

	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "remove", barSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "remove", fooSet}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeRemove(context.Background(), &out, WorktreeRemoveOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	// Set dir should be gone.
	if dirExists(setDir) {
		t.Errorf("set dir should be removed; still exists: %s", setDir)
	}

	// Verify no branch-delete calls.
	for _, c := range f.Calls() {
		if contains([]byte(strings.Join(c.Args, " ")), "branch") && contains([]byte(strings.Join(c.Args, " ")), "-d") {
			t.Errorf("WorktreeRemove must NOT delete branches; got call: %v", c.Args)
		}
	}
}

// ============================================================
// WorktreeRemove — --force is passed through
// ============================================================

func TestWorktreeRemove_ForceFlag(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")
	if err := os.MkdirAll(barSet, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fooSet, 0o755); err != nil {
		t.Fatal(err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")

	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "remove", barSet, "--force"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "remove", fooSet, "--force"}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeRemove(context.Background(), &out, WorktreeRemoveOptions{Branch: "feature", Force: true}); err != nil {
		t.Fatalf("WorktreeRemove --force: %v", err)
	}

	// Verify --force was passed.
	forceSeen := 0
	for _, c := range f.Calls() {
		if len(c.Args) > 3 && c.Args[3] == "remove" {
			for _, a := range c.Args {
				if a == "--force" {
					forceSeen++
				}
			}
		}
	}
	if forceSeen != 2 {
		t.Errorf("expected --force in both worktree remove calls; saw it %d times", forceSeen)
	}
}

// ============================================================
// WorktreeRemove — without --force, flag NOT appended
// ============================================================

func TestWorktreeRemove_NoForceFlag(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")
	if err := os.MkdirAll(barSet, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fooSet, 0o755); err != nil {
		t.Fatal(err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")

	// Register responses WITHOUT --force.
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "remove", barSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "remove", fooSet}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeRemove(context.Background(), &out, WorktreeRemoveOptions{Branch: "feature", Force: false}); err != nil {
		t.Fatalf("WorktreeRemove (no force): %v", err)
	}

	// Verify --force was NOT appended.
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "--force" {
				t.Errorf("--force must not be appended when Force=false; args=%v", c.Args)
			}
		}
	}
}

// ============================================================
// WorktreeRemove — set dir missing → error
// ============================================================

func TestWorktreeRemove_SetDirMissing(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = w.WorktreeRemove(context.Background(), &bytes.Buffer{}, WorktreeRemoveOptions{Branch: "nonexistent"})
	if err == nil {
		t.Fatal("expected error when set dir doesn't exist, got nil")
	}
}

// ============================================================
// WorktreePrune
// ============================================================

func TestWorktreePrune_RunsInEachRepo(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")

	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "prune"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "prune"}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreePrune(context.Background(), &out, WorktreePruneOptions{}); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}

	calls := f.Calls()
	var pruneCalls []exec.Call
	for _, c := range calls {
		if len(c.Args) > 3 && c.Args[3] == "prune" {
			pruneCalls = append(pruneCalls, c)
		}
	}
	if len(pruneCalls) != 2 {
		t.Errorf("expected 2 worktree prune calls, got %d", len(pruneCalls))
	}
	// Verify streaming output.
	for _, c := range pruneCalls {
		if c.Opts.Stdout == nil {
			t.Errorf("worktree prune should stream output; args=%v", c.Args)
		}
	}
}

// ============================================================
// WorktreeList
// ============================================================

func TestWorktreeList_ListsSets(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create two fake set dirs under worktrees_dir.
	wtDir := w.WorktreesDir()
	if err := os.MkdirAll(filepath.Join(wtDir, "feature-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtDir, "feature-b"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := w.WorktreeList(context.Background(), &out, WorktreeListOptions{}); err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "feature-a") {
		t.Errorf("expected 'feature-a' in list output; got: %q", output)
	}
	if !strings.Contains(output, "feature-b") {
		t.Errorf("expected 'feature-b' in list output; got: %q", output)
	}
	// No git calls needed for list.
	_ = f.Calls()
}

func TestWorktreeList_NoWorktreesDir(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Do NOT create worktrees_dir — expect no error and empty output.
	var out bytes.Buffer
	if err := w.WorktreeList(context.Background(), &out, WorktreeListOptions{}); err != nil {
		t.Fatalf("WorktreeList with no dir: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected empty output when worktrees_dir missing; got: %q", out.String())
	}
}

// ============================================================
// WorktreeAdd — revs file absent is OK (not copied, not error)
// ============================================================

func TestWorktreeAdd_NoRevsFile(t *testing.T) {
	root, f := makeTwoRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	writeFile(t, filepath.Join(root, ConfigFileName), `[repos.foo]
url = "github:owner/foo"
[repos.bar]
url = "github:owner/bar"
`)
	writeFile(t, filepath.Join(root, LockFileName), `{"order":[],"repos":{},"edges":[]}`)
	// Deliberately do NOT write RevLockFileName.

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")
	setDir := filepath.Join(w.WorktreesDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")

	addWorktreeListClean(f, barCanonical, "bar")
	addWorktreeListClean(f, fooCanonical, "foo")
	addBranchNotExists(f, barCanonical, "feature")
	addBranchNotExists(f, fooCanonical, "feature")
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "add", "-b", "feature", fooSet}, exec.Result{}, nil)

	var out bytes.Buffer
	if err := w.WorktreeAdd(context.Background(), &out, WorktreeAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorktreeAdd without revs file: %v", err)
	}

	// RevLock should NOT be in the set dir (was absent in canonical).
	if fileExists(filepath.Join(setDir, RevLockFileName)) {
		t.Errorf("RevLockFileName should not be copied when absent in canonical")
	}
}
