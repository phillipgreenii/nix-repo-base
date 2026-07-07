package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// nixCalls filters a FakeRunner's recorded calls to the `nix` invocations.
func nixCalls(f *exec.FakeRunner) []exec.Call {
	var out []exec.Call
	for _, c := range f.Calls() {
		if c.Name == "nix" {
			out = append(out, c)
		}
	}
	return out
}

// TestWorkforestAdd_InstallsOptInHooksInWorktree verifies that, after the
// worktree adds, only the opted-in repo (bar) triggers exactly one
// `nix run .#install-pre-commit-hooks`, and it runs IN bar's set worktree dir.
// The non-opted-in repo (foo) produces no install call.
func TestWorkforestAdd_InstallsOptInHooksInWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
install-hooks = ["install-pre-commit-hooks"]
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["bar","foo"],
  "repos": {"foo": {"remote_url": "github:owner/foo"}, "bar": {"remote_url": "github:owner/bar"}},
  "edges": []
}`)
	f := exec.NewFakeRunner()
	makeFakeCanonicalRepos(t, root, "bar", "foo")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	fooCanonical := filepath.Join(root, "foo")
	setDir := filepath.Join(w.WorkforestsDir(), "feature")
	barSet := filepath.Join(setDir, "bar")
	fooSet := filepath.Join(setDir, "foo")

	addWorktreeListClean(f, barCanonical, "bar")
	addWorktreeListClean(f, fooCanonical, "foo")
	addBranchNotExists(f, barCanonical, "feature")
	addBranchNotExists(f, fooCanonical, "feature")
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", fooCanonical, "worktree", "add", "-b", "feature", fooSet}, exec.Result{}, nil)
	// Only bar opts in → exactly one nix install call is expected.
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorkforestAdd: %v", err)
	}
	if errOut.Len() != 0 {
		t.Errorf("expected empty errOut on happy path; got %q", errOut.String())
	}

	nc := nixCalls(f)
	if len(nc) != 1 {
		t.Fatalf("expected exactly 1 nix install call (only opted-in bar), got %d: %+v", len(nc), nc)
	}
	if nc[0].Opts.Dir != barSet {
		t.Errorf("install-hooks must run in bar's set worktree; Dir=%q want %q", nc[0].Opts.Dir, barSet)
	}
	if got, want := strings.Join(nc[0].Args, " "), strings.Join(installArgs("install-pre-commit-hooks"), " "); got != want {
		t.Errorf("nix args: got %q want %q", got, want)
	}
}

// TestWorkforestAdd_InstallHookFailureIsWarnOnlyNonFatal verifies that a failed
// install-hooks run in a worktree does NOT abort/rollback `workforest add`: the
// call returns nil, a warning naming the repo is written to errOut, and the
// set's config is still written (the worktrees are already created).
func TestWorkforestAdd_InstallHookFailureIsWarnOnlyNonFatal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[repos.bar]
url = "github:owner/bar"
install-hooks = ["install-pre-commit-hooks"]
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "order": ["bar"],
  "repos": {"bar": {"remote_url": "github:owner/bar"}},
  "edges": []
}`)
	f := exec.NewFakeRunner()
	makeFakeCanonicalRepos(t, root, "bar")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	barCanonical := filepath.Join(root, "bar")
	setDir := filepath.Join(w.WorkforestsDir(), "feature")
	barSet := filepath.Join(setDir, "bar")

	addWorktreeListClean(f, barCanonical, "bar")
	addBranchNotExists(f, barCanonical, "feature")
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet}, exec.Result{}, nil)
	// install-hooks FAILS — must be warn-only, never fatal.
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"),
		exec.Result{ExitCode: 1},
		&exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 1}})

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorkforestAdd must succeed despite install-hooks failure; got %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: install-hooks in workforest worktree bar") {
		t.Errorf("expected a warn-only message naming repo bar; got errOut=%q", errOut.String())
	}
	// The add did not roll back: the set config was still written.
	if !fileExists(filepath.Join(setDir, ConfigFileName)) {
		t.Errorf("set config should be written even when install-hooks failed (add is not rolled back)")
	}
}

// TestWorkforestAddRepo_InstallsOptInHooksInWorktree verifies that adding a
// participating repo (lib, opted-in) to an existing set runs its install-hooks
// in the new worktree dir.
func TestWorkforestAddRepo_InstallsOptInHooksInWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[workspace]
terminal = "app"

[repos.app]
url = "github:owner/app"

[repos.lib]
url = "github:owner/lib"
install-hooks = ["install-pre-commit-hooks"]
`)
	writeFile(t, filepath.Join(root, LockFileName), `{
  "terminal": "app",
  "order": ["lib","app"],
  "repos": {
    "app": {"remote_url": "github:owner/app", "flake_path": "flake.nix"},
    "lib": {"remote_url": "github:owner/lib", "flake_path": "flake.nix"}
  },
  "edges": []
}`)
	f := exec.NewFakeRunner()
	makeFakeCanonicalRepos(t, root, "app", "lib")

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Existing set has only app; add opted-in "lib".
	setDir := seedSubsetSet(t, w, "feature", "app")
	libCanonical := filepath.Join(root, "lib")
	libSet := filepath.Join(setDir, "lib")

	addWorktreeListClean(f, libCanonical, "lib")
	addBranchExists(f, libCanonical, "feature")
	f.AddResponse("git", []string{"-C", libCanonical, "worktree", "add", libSet, "feature"}, exec.Result{}, nil)
	f.AddResponse("nix", installArgs("install-pre-commit-hooks"), exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAddRepo(context.Background(), &out, &errOut, WorkforestAddRepoOptions{Branch: "feature", Repo: "lib"}); err != nil {
		t.Fatalf("WorkforestAddRepo: %v", err)
	}

	nc := nixCalls(f)
	if len(nc) != 1 {
		t.Fatalf("expected exactly 1 nix install call for the added repo lib, got %d: %+v", len(nc), nc)
	}
	if nc[0].Opts.Dir != libSet {
		t.Errorf("install-hooks must run in lib's set worktree; Dir=%q want %q", nc[0].Opts.Dir, libSet)
	}

	members := setMembers(t, setDir)
	if !members["lib"] || !members["app"] {
		t.Errorf("set should contain app+lib after add-repo; got %v", members)
	}
}
