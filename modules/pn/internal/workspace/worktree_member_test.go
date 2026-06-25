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

// seedSubsetSet writes a set directory containing a subset membership
// (config/lock/revs filtered to members) and a fake repo subdir per member,
// as `worktree add --repos` would produce. Returns the set dir.
func seedSubsetSet(t *testing.T, w *Workspace, branch string, members ...string) string {
	t.Helper()
	setDir := filepath.Join(w.WorktreesDir(), branch)
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m] = true
		if err := os.MkdirAll(filepath.Join(setDir, m), 0o755); err != nil {
			t.Fatalf("seed set repo %s: %v", m, err)
		}
	}
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatalf("seed set dir: %v", err)
	}
	if err := writeConfigTOMLTo(filepath.Join(setDir, ConfigFileName), filterConfig(w.Config(), memberSet)); err != nil {
		t.Fatalf("seed set config: %v", err)
	}
	if err := WriteLock(filepath.Join(setDir, LockFileName), filterLock(w.Lock(), memberSet)); err != nil {
		t.Fatalf("seed set lock: %v", err)
	}
	return setDir
}

// setMembers reads the member repo keys from a set's pn-workspace.toml.
func setMembers(t *testing.T, setDir string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(setDir, ConfigFileName))
	if err != nil {
		t.Fatalf("read set config: %v", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("parse set config: %v", err)
	}
	m := map[string]bool{}
	for k := range cfg.Repos {
		m[k] = true
	}
	return m
}

// ============================================================
// WorktreeAddRepo
// ============================================================

func TestWorktreeAddRepo_HappyPath(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Existing set has only app+lib; add "other".
	setDir := seedSubsetSet(t, w, "feature", "app", "lib")
	otherCanonical := filepath.Join(root, "other")
	otherSet := filepath.Join(setDir, "other")

	// other's branch is checked out somewhere? No — clean; branch exists already
	// (the set's branch was created when the set was made), so check-out form.
	addWorktreeListClean(f, otherCanonical, "other")
	addBranchExists(f, otherCanonical, "feature")
	f.AddResponse("git", []string{"-C", otherCanonical, "worktree", "add", otherSet, "feature"}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorktreeAddRepo(context.Background(), &out, &errOut, WorktreeAddRepoOptions{Branch: "feature", Repo: "other"}); err != nil {
		t.Fatalf("WorktreeAddRepo: %v", err)
	}

	// Set membership must now include other (3 members).
	members := setMembers(t, setDir)
	if !members["other"] || !members["app"] || !members["lib"] {
		t.Errorf("set should contain app+lib+other after add-repo; got %v", members)
	}
}

func TestWorktreeAddRepo_AlreadyPresentErrors(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = seedSubsetSet(t, w, "feature", "app", "lib")

	err = w.WorktreeAddRepo(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, WorktreeAddRepoOptions{Branch: "feature", Repo: "lib"})
	if err == nil {
		t.Fatal("expected error adding a repo already in the set, got nil")
	}
	if !strings.Contains(err.Error(), "lib") {
		t.Errorf("error should name 'lib'; got: %v", err)
	}
}

func TestWorktreeAddRepo_UnknownRepoErrors(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = seedSubsetSet(t, w, "feature", "app", "lib")

	err = w.WorktreeAddRepo(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, WorktreeAddRepoOptions{Branch: "feature", Repo: "ghost"})
	if err == nil {
		t.Fatal("expected error adding unknown repo, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name 'ghost'; got: %v", err)
	}
}

func TestWorktreeAddRepo_SetMissingErrors(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.WorktreeAddRepo(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, WorktreeAddRepoOptions{Branch: "nope", Repo: "lib"})
	if err == nil {
		t.Fatal("expected error when set does not exist, got nil")
	}
}

// ============================================================
// WorktreeRemoveRepo
// ============================================================

func TestWorktreeRemoveRepo_HappyPath(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Set has app+lib; remove lib.
	setDir := seedSubsetSet(t, w, "feature", "app", "lib")
	libCanonical := filepath.Join(root, "lib")
	libSet := filepath.Join(setDir, "lib")

	f.AddResponse("git", []string{"-C", libCanonical, "worktree", "remove", libSet}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorktreeRemoveRepo(context.Background(), &out, &errOut, WorktreeRemoveRepoOptions{Branch: "feature", Repo: "lib"}); err != nil {
		t.Fatalf("WorktreeRemoveRepo: %v", err)
	}

	members := setMembers(t, setDir)
	if members["lib"] {
		t.Errorf("lib should be removed from set membership; got %v", members)
	}
	if !members["app"] {
		t.Errorf("app should remain in set; got %v", members)
	}
}

func TestWorktreeRemoveRepo_ForceFlag(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	setDir := seedSubsetSet(t, w, "feature", "app", "lib")
	libCanonical := filepath.Join(root, "lib")
	libSet := filepath.Join(setDir, "lib")

	f.AddResponse("git", []string{"-C", libCanonical, "worktree", "remove", libSet, "--force"}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorktreeRemoveRepo(context.Background(), &out, &errOut, WorktreeRemoveRepoOptions{Branch: "feature", Repo: "lib", Force: true}); err != nil {
		t.Fatalf("WorktreeRemoveRepo --force: %v", err)
	}
	forceSeen := false
	for _, c := range f.Calls() {
		for _, a := range c.Args {
			if a == "--force" {
				forceSeen = true
			}
		}
	}
	if !forceSeen {
		t.Errorf("--force should be passed through to git worktree remove")
	}
}

func TestWorktreeRemoveRepo_NotInSetErrors(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = seedSubsetSet(t, w, "feature", "app", "lib")

	err = w.WorktreeRemoveRepo(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, WorktreeRemoveRepoOptions{Branch: "feature", Repo: "other"})
	if err == nil {
		t.Fatal("expected error removing a repo not in the set, got nil")
	}
	if !strings.Contains(err.Error(), "other") {
		t.Errorf("error should name 'other'; got: %v", err)
	}
}

func TestWorktreeRemoveRepo_RefusesLastRepo(t *testing.T) {
	root, f := makeThreeRepoWorkspace(t)
	makeFakeCanonicalRepos(t, root, "app", "lib", "other")
	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Set has only "app".
	_ = seedSubsetSet(t, w, "feature", "app")

	err = w.WorktreeRemoveRepo(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, WorktreeRemoveRepoOptions{Branch: "feature", Repo: "app"})
	if err == nil {
		t.Fatal("expected error removing the last repo from a set, got nil")
	}
	if !strings.Contains(err.Error(), "last") {
		t.Errorf("error should mention it is the last repo; got: %v", err)
	}
	// No git worktree remove should have run.
	for _, c := range f.Calls() {
		if len(c.Args) > 3 && c.Args[3] == "remove" {
			t.Errorf("no git worktree remove should run when refusing last-repo removal; got: %v", c.Args)
		}
	}
}
