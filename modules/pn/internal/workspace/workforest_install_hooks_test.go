package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// shCalls filters a FakeRunner's recorded calls to the `sh` invocations (how
// per-repo event hooks execute).
func shCalls(f *exec.FakeRunner) []exec.Call {
	var out []exec.Call
	for _, c := range f.Calls() {
		if c.Name == "sh" {
			out = append(out, c)
		}
	}
	return out
}

// nixRunHookCmd is the sh command an override-free {nix_run install-pre-commit-hooks}
// hook expands to for a repo whose set-worktree flake dir is setRepo.
func nixRunHookCmd(setRepo string) string {
	return "nix run '" + setRepo + "#install-pre-commit-hooks'"
}

// TestWorkforestAdd_FiresPostCloneHookInWorktree verifies that, after the
// worktree adds, only the repo (bar) declaring a post-clone hook triggers
// exactly one `sh -c "nix run …#install-pre-commit-hooks"`, running IN bar's
// set worktree dir. The repo without the hook (foo) produces no call.
func TestWorkforestAdd_FiresPostCloneHookInWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[repos.foo]
url = "github:owner/foo"

[repos.bar]
url = "github:owner/bar"
[[repos.bar.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
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

	trustWS(t, root) // trust the canonical so post-clone hooks propagate + fire
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
	// Only bar declares a post-clone hook → exactly one sh install call expected.
	f.AddResponse("sh", []string{"-c", nixRunHookCmd(barSet)}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorkforestAdd: %v", err)
	}

	sc := shCalls(f)
	if len(sc) != 1 {
		t.Fatalf("expected exactly 1 sh hook call (only bar has post-clone), got %d: %+v", len(sc), sc)
	}
	if sc[0].Opts.Dir != barSet {
		t.Errorf("post-clone hook must run in bar's set worktree; Dir=%q want %q", sc[0].Opts.Dir, barSet)
	}
}

// TestWorkforestAdd_PostCloneHookFailureIsWarnOnlyNonFatal verifies that a
// failed post-clone hook does NOT abort/rollback `workforest add`: the call
// returns nil (warn-only) and the set's config is still written.
func TestWorkforestAdd_PostCloneHookFailureIsWarnOnlyNonFatal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[repos.bar]
url = "github:owner/bar"
[[repos.bar.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
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

	trustWS(t, root) // trust the canonical so the post-clone hook propagates + fires
	barCanonical := filepath.Join(root, "bar")
	setDir := filepath.Join(w.WorkforestsDir(), "feature")
	barSet := filepath.Join(setDir, "bar")

	addWorktreeListClean(f, barCanonical, "bar")
	addBranchNotExists(f, barCanonical, "feature")
	f.AddResponse("git", []string{"-C", barCanonical, "worktree", "add", "-b", "feature", barSet}, exec.Result{}, nil)
	// The post-clone hook FAILS — must be warn-only (to os.Stderr), never fatal.
	f.AddResponse("sh", []string{"-c", nixRunHookCmd(barSet)},
		exec.Result{ExitCode: 1},
		&exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 1}})

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorkforestAdd must succeed despite post-clone hook failure; got %v", err)
	}
	// The failing hook still ran, and the add did not roll back (set config written).
	if len(shCalls(f)) != 1 {
		t.Errorf("expected the failing hook to have run once; got %d", len(shCalls(f)))
	}
	if !fileExists(filepath.Join(setDir, ConfigFileName)) {
		t.Errorf("set config should be written even when a post-clone hook failed (add is not rolled back)")
	}
}

// TestWorkforestAddRepo_FiresPostCloneHookInWorktree verifies that adding a repo
// (lib, with a post-clone hook) to an existing set runs its hook in the new
// worktree dir.
func TestWorkforestAddRepo_FiresPostCloneHookInWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[workspace]
terminal = "app"

[repos.app]
url = "github:owner/app"

[repos.lib]
url = "github:owner/lib"
[[repos.lib.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
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

	trustWS(t, root) // trust the canonical so the added repo's post-clone hook fires
	// Existing set has only app; add "lib" (which declares a post-clone hook).
	setDir := seedSubsetSet(t, w, "feature", "app")
	libCanonical := filepath.Join(root, "lib")
	libSet := filepath.Join(setDir, "lib")

	addWorktreeListClean(f, libCanonical, "lib")
	addBranchExists(f, libCanonical, "feature")
	f.AddResponse("git", []string{"-C", libCanonical, "worktree", "add", libSet, "feature"}, exec.Result{}, nil)
	f.AddResponse("sh", []string{"-c", nixRunHookCmd(libSet)}, exec.Result{}, nil)

	var out, errOut bytes.Buffer
	if err := w.WorkforestAddRepo(context.Background(), &out, &errOut, WorkforestAddRepoOptions{Branch: "feature", Repo: "lib"}); err != nil {
		t.Fatalf("WorkforestAddRepo: %v", err)
	}

	sc := shCalls(f)
	if len(sc) != 1 {
		t.Fatalf("expected exactly 1 sh hook call for the added repo lib, got %d: %+v", len(sc), sc)
	}
	if sc[0].Opts.Dir != libSet {
		t.Errorf("post-clone hook must run in lib's set worktree; Dir=%q want %q", sc[0].Opts.Dir, libSet)
	}

	members := setMembers(t, setDir)
	if !members["lib"] || !members["app"] {
		t.Errorf("set should contain app+lib after add-repo; got %v", members)
	}
}

// TestWorkforestAdd_UntrustedCanonicalWarnSkipsPostClone verifies that when the
// canonical workspace is NOT trusted, trust is not propagated to the derived
// set and its post-clone hook is warn-skipped (no `sh`, add still succeeds).
// (bead pg2-oymai)
func TestWorkforestAdd_UntrustedCanonicalWarnSkipsPostClone(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // isolated; canonical NOT trusted
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ConfigFileName), `
[repos.bar]
url = "github:owner/bar"
[[repos.bar.hooks]]
when = ["post-clone"]
run = ["{nix_run install-pre-commit-hooks}"]
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
	// No `sh` response scripted: the hook must be trust-skipped, not executed.

	var out, errOut bytes.Buffer
	if err := w.WorkforestAdd(context.Background(), &out, &errOut, WorkforestAddOptions{Branch: "feature"}); err != nil {
		t.Fatalf("WorkforestAdd must succeed (post-clone warn-only); got %v", err)
	}
	if n := len(shCalls(f)); n != 0 {
		t.Errorf("post-clone hook must be trust-skipped on an untrusted canonical; got %d sh calls", n)
	}
	if !fileExists(filepath.Join(setDir, ConfigFileName)) {
		t.Errorf("set config should still be written")
	}
}
