package workspace

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestPrimaryMainState(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[repos.foo]\nurl = \"github:o/foo\"\n")
	foo := filepath.Join(root, "foo")

	cases := []struct {
		name   string
		branch string
		exit   int // exit code of diff --quiet (0 clean, 1 dirty)
		want   primaryState
	}{
		{"clean main", "main", 0, primaryOnCleanMain},
		{"other branch", "feature-x", 0, primaryOnOtherBranch},
		{"dirty main", "main", 1, primaryOnDirtyMain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := exec.NewFakeRunner()
			f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"},
				exec.Result{Stdout: []byte(tc.branch + "\n")}, nil)
			if tc.branch == "main" {
				var derr error
				if tc.exit != 0 {
					derr = &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: tc.exit}}
				}
				f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: tc.exit}, derr)
				if tc.exit == 0 {
					f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
				}
			}
			w, _ := Open(root, f)
			if got := w.primaryMainState(context.Background(), foo); got != tc.want {
				t.Errorf("primaryMainState = %v, want %v", got, tc.want)
			}
		})
	}
}

// wtUpdateFixture sets up a single-repo (terminal "foo") workspace and returns
// the root, the primary dir, the worktree dir for stamp "TEST", and the runner.
func wtUpdateFixture(t *testing.T) (root, foo, wt string, f *exec.FakeRunner) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	foo = filepath.Join(root, "foo")
	wt = filepath.Join(root, ".worktrees", updateWorktreesSubdir, "foo-TEST")
	f = exec.NewFakeRunner()
	return root, foo, wt, f
}

// scriptThroughPush scripts steps 1–6 for repo "foo" (worktree add → … → push).
func scriptThroughPush(f *exec.FakeRunner, foo, wt, branch string) {
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("dead00000000000000000000000000000000beef\n")}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"}, exec.Result{}, nil)
}

func TestUpdateViaWorktree_HappyPath_CleanMain(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/nix/store/x/lib/scripts"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	rl, _ := ReadRevLock(filepath.Join(root, RevLockFileName))
	if rl.Repos["foo"].Rev != "dead00000000000000000000000000000000beef" {
		t.Errorf("revs.json rev = %q, want pushed tip", rl.Repos["foo"].Rev)
	}
}

func TestUpdateViaWorktree_PushSucceedsFfDefers(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"})
	if err == nil {
		t.Fatalf("expected non-nil error (deferred), got nil")
	}
	rl, _ := ReadRevLock(filepath.Join(root, RevLockFileName))
	if rl.Repos["foo"].Rev != "dead00000000000000000000000000000000beef" {
		t.Errorf("revs.json must record pushed rev even on defer; got %q", rl.Repos["foo"].Rev)
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[2] == "worktree" && c.Args[1] == foo && stringsContain(c.Args, "remove") {
			t.Fatalf("must not remove worktree on defer")
		}
	}
}

func stringsContain(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestUpdateViaWorktree_OtherBranchRefFf(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "fetch", ".", branch + ":main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestUpdateViaWorktree_DirtyMainDefers(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error")
	}
}

func TestUpdateViaWorktree_RebaseConflictAborts(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", wt, "rebase", "--abort"}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected failure")
	}
	if !calledWith(f, "git", []string{"-C", wt, "rebase", "--abort"}) {
		t.Fatalf("expected rebase --abort after conflict")
	}
}

func TestUpdateViaWorktree_EmptyULLibDirIsFatal(t *testing.T) {
	t.Setenv("UL_LIB_DIR", "")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), "[workspace]\nterminal=\"foo\"\n[repos.foo]\nurl=\"github:o/foo\"\n")
	f := exec.NewFakeRunner()
	// Script ResolveULLibDir's nix call to return empty (so the fatal path is hit).
	f.AddResponse("nix", []string{"run", ulLibResolverRef}, exec.Result{Stdout: []byte("")}, nil)
	w, _ := Open(root, f)
	err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "UL_LIB_DIR") {
		t.Fatalf("expected UL_LIB_DIR hard error, got %v", err)
	}
}

func calledWith(f *exec.FakeRunner, name string, args []string) bool {
	for _, c := range f.Calls() {
		if c.Name == name && len(c.Args) == len(args) {
			ok := true
			for i := range args {
				if c.Args[i] != args[i] {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}
