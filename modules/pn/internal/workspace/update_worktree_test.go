package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
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
	wt = filepath.Join(root, ".workforests", updateWorktreesSubdir, "foo-TEST")
	mkUpdateLocks(t, wt) // existence-gate: the worktree carries the committed script
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
}

// TestUpdateViaWorktree_AbortsRunOnResourceExit77: when a repo's update-locks.sh
// exits ulExitAbort (77 = environmental/resource failure, e.g. ENOSPC), the run
// must STOP — the later repo is never attempted — and the error must say so.
func TestUpdateViaWorktree_AbortsRunOnResourceExit77(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "zzz"

[repos.aaa]
url = "github:owner/aaa"

[repos.zzz]
url = "github:owner/zzz"
`)
	aaa := filepath.Join(root, "aaa")
	zzz := filepath.Join(root, "zzz")
	wtAaa := filepath.Join(root, ".workforests", updateWorktreesSubdir, "aaa-TEST")
	mkUpdateLocks(t, wtAaa) // aaa runs first (alphabetical); zzz must never run

	f := exec.NewFakeRunner()
	f.AddResponse("git", []string{"-C", aaa, "worktree", "add", "-b", branch, wtAaa, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wtAaa, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wtAaa, "rebase", "origin/main"}, exec.Result{}, nil)
	// update-locks.sh aborts with 77 (captured stderr shows the ENOSPC cause).
	f.AddResponse("./update-locks.sh", nil,
		exec.Result{ExitCode: ulExitAbort, Stderr: []byte("error: write of 9 bytes: No space left on device")},
		&exec.CommandError{Name: "./update-locks.sh", Result: exec.Result{ExitCode: ulExitAbort, Stderr: []byte("No space left on device")}})
	// zzz: intentionally NOT scripted — the run must abort before touching it.

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"})
	if err == nil {
		t.Fatal("expected an abort error, got nil")
	}
	if !strings.Contains(err.Error(), "abort") {
		t.Errorf("error should mention the abort; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "aaa") {
		t.Errorf("error should name the aborting repo (aaa); got %q", err.Error())
	}
	for _, c := range f.Calls() {
		joined := c.Name + " " + strings.Join(c.Args, " ")
		if strings.Contains(joined, zzz) {
			t.Fatalf("zzz must not be attempted after an abort; saw call: %s", joined)
		}
	}
}

// TestUpdateViaWorktree_SiblingsOnly_SkipsUpdateLocksAndULLibDir: with
// SiblingsOnly the worktree flow MUST skip step-4 update-locks.sh (even though
// the script exists on disk) AND MUST NOT resolve/require UL_LIB_DIR (the
// resolver is never called and an empty env is not fatal), since update-locks
// never runs. Everything else — worktree isolate → propagate → rebase → push →
// ff-integrate → cleanup — runs unchanged.
func TestUpdateViaWorktree_SiblingsOnly_SkipsUpdateLocksAndULLibDir(t *testing.T) {
	t.Setenv("UL_LIB_DIR", "") // empty would be fatal in normal mode; SiblingsOnly must not care
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	// Steps 1–6 WITHOUT scripting ./update-locks.sh (it must be skipped).
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"}, exec.Result{}, nil)
	// Step 7: clean-main integration.
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)

	w, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// No ULLibDir supplied: SiblingsOnly must not require it.
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{SiblingsOnly: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "./update-locks.sh" {
			t.Fatalf("--siblings-only must NOT run update-locks.sh")
		}
	}
	if calledWith(f, "nix", []string{"run", ulLibResolverRef}) {
		t.Fatalf("--siblings-only must NOT resolve UL_LIB_DIR (update-locks never runs)")
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
	var out bytes.Buffer
	err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"})
	if err == nil {
		t.Fatalf("expected non-nil error (deferred), got nil")
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[2] == "worktree" && c.Args[1] == foo && slices.Contains(c.Args, "remove") {
			t.Fatalf("must not remove worktree on defer")
		}
	}
	// The asymmetric-defer recovery is a RESET to the pushed remote, not a merge
	// (ADR 0009 N1). The run must surface that hint so the user doesn't re-merge.
	s := out.String()
	if !strings.Contains(s, "deferred") {
		t.Errorf("summary should mark foo deferred; got:\n%s", s)
	}
	if !strings.Contains(s, "reset --hard origin/main") {
		t.Errorf("defer must print the reset-not-merge recovery hint; got:\n%s", s)
	}
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

// scriptDirtyMainProbe scripts the step-8 state probe for a dirty primary main:
// HEAD==main and `diff --quiet` reports dirty (exit 1), classifying as
// primaryOnDirtyMain. (The `diff --cached --quiet` probe is short-circuited by
// the dirty modified tree, so it is not issued.)
func scriptDirtyMainProbe(f *exec.FakeRunner, foo string) {
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
}

// TestUpdateViaWorktree_DirtyMainFfFirstSucceeds: dirty primary main, but the
// FIRST `merge --ff-only` succeeds (the dirty file does not collide with the
// ff'd paths — the common lock-file case). No autostash is issued; the worktree
// + branch are removed and the run succeeds, recording the pushed rev.
func TestUpdateViaWorktree_DirtyMainFfFirstSucceeds(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err != nil {
		t.Fatalf("dirty main with a clean ff should succeed; got %v", err)
	}
	// No stash should be issued when the first ff succeeds.
	for _, c := range f.Calls() {
		if c.Name == "git" && slices.Contains(c.Args, "stash") {
			t.Errorf("must not stash when the first ff succeeds; got %v", c.Args)
		}
	}
}

// TestUpdateViaWorktree_DirtyMainCollidesAutostashes: dirty primary main; the
// first `merge --ff-only` FAILS (dirty file collides with an ff'd path), so the
// flow autostashes, retries the ff (succeeds), pops the stash, and cleans up.
// The two `merge --ff-only` scripts are consumed FIFO: fail then OK.
func TestUpdateViaWorktree_DirtyMainCollidesAutostashes(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // first ff FAILS (collision)
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "push", "-m", "pn-update autostash " + branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil) // retry OK
	f.AddResponse("git", []string{"-C", foo, "stash", "pop"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "branch", "-d", branch}, exec.Result{}, nil)

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err != nil {
		t.Fatalf("dirty main collision should autostash and succeed; got %v", err)
	}
	// A successful autostash round-trip MUST still clean up: assert the ephemeral
	// worktree was removed (guards a regression that reports OK but skips cleanup).
	removedWorktree := false
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 5 && c.Args[1] == foo && c.Args[2] == "worktree" && c.Args[3] == "remove" && c.Args[4] == wt {
			removedWorktree = true
		}
	}
	if !removedWorktree {
		t.Errorf("autostash success must remove the worktree via `git -C %s worktree remove %s`; calls:\n%v", foo, wt, f.Calls())
	}
}

// TestUpdateViaWorktree_DirtyMainStashFails: first ff fails, then `stash push`
// itself fails → the run defers at "integrate", leaving the worktree behind.
func TestUpdateViaWorktree_DirtyMainStashFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // first ff FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "push", "-m", "pn-update autostash " + branch}, // stash FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error when autostash push fails")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "integrate")
}

// TestUpdateViaWorktree_DirtyMainFfFailsAfterStash: first ff fails, stash push
// OK, but the RETRY ff still fails (remote advanced / diverged, not a dirty-file
// issue) → restore the stash (`stash pop`) and defer at "ff-merge" with the
// reset-not-merge recovery hint; the worktree is left behind.
func TestUpdateViaWorktree_DirtyMainFfFailsAfterStash(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // first ff FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "push", "-m", "pn-update autostash " + branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // retry ff FAILS (not fast-forwardable)
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "pop"}, exec.Result{}, nil)

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error when the retry ff is not fast-forwardable")
	}
	s := out.String()
	if !strings.Contains(s, "reset --hard origin/main") {
		t.Errorf("ff-merge defer must print the reset-not-merge recovery hint; got:\n%s", s)
	}
	// The user's autostashed tree MUST be restored before deferring: assert the
	// restore `stash pop` was actually issued (guards against dropping the pop and
	// leaving primary main with the user's changes stashed away).
	poppedStash := false
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 4 && c.Args[1] == foo && c.Args[2] == "stash" && c.Args[3] == "pop" {
			poppedStash = true
		}
	}
	if !poppedStash {
		t.Errorf("ff-merge defer must restore the autostash via `git -C %s stash pop`; calls:\n%v", foo, f.Calls())
	}
	assertLeftBehind(t, f, s, foo, wt, "ff-merge")
}

// TestUpdateViaWorktree_DirtyMainFfFailsAfterStash_PopAlsoFails: first ff fails,
// stash push OK, the RETRY ff still fails (not fast-forwardable), AND the restore
// `stash pop` itself fails → still defer at "ff-merge", but the note MUST point
// the user at `git stash list` to recover their stranded changes and MUST NOT
// falsely claim the stash was restored.
func TestUpdateViaWorktree_DirtyMainFfFailsAfterStash_PopAlsoFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // first ff FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "push", "-m", "pn-update autostash " + branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // retry ff FAILS (not fast-forwardable)
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "pop"}, // restore pop ALSO FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error when the retry ff and the restore pop both fail")
	}
	s := out.String()
	// The retry ff was attempted, so the restore `stash pop` must have been issued.
	poppedStash := false
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 4 && c.Args[1] == foo && c.Args[2] == "stash" && c.Args[3] == "pop" {
			poppedStash = true
		}
	}
	if !poppedStash {
		t.Errorf("ff-merge defer must still attempt the restore `git -C %s stash pop`; calls:\n%v", foo, f.Calls())
	}
	// The pop failed, so the note MUST point at the retained stash for recovery.
	if !strings.Contains(s, "stash list") {
		t.Errorf("restore-pop failure must surface the retained-stash recovery hint (`git stash list`); got:\n%s", s)
	}
	// The message MUST NOT falsely claim the stash was restored.
	if strings.Contains(s, "stash restored") {
		t.Errorf("restore-pop failure must not claim the stash was restored; got:\n%s", s)
	}
	assertLeftBehind(t, f, s, foo, wt, "ff-merge")
}

// TestUpdateViaWorktree_DirtyMainPopConflicts: first ff fails, stash push OK,
// retry ff OK (integration landed), but `stash pop` CONFLICTS → HARD DEFER at
// "autostash-pop". This is the silent-corruption guard: the run must NOT be OK
// and the worktree must NOT be removed, and the note must point at the retained
// stash so the user can recover.
func TestUpdateViaWorktree_DirtyMainPopConflicts(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	scriptDirtyMainProbe(f, foo)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, // first ff FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", foo, "stash", "push", "-m", "pn-update autostash " + branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil) // retry OK
	f.AddResponse("git", []string{"-C", foo, "stash", "pop"},                                   // pop CONFLICTS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error when autostash pop conflicts")
	}
	s := out.String()
	// MUST NOT report OK for foo.
	if strings.Contains(s, "✓ foo") {
		t.Errorf("pop conflict must not report foo ok; got:\n%s", s)
	}
	// MUST surface the retained-stash recovery hint.
	if !strings.Contains(s, "stash list") {
		t.Errorf("pop-conflict note must mention the retained stash; got:\n%s", s)
	}
	if !strings.Contains(s, "autostash-pop") {
		t.Errorf("summary should name the autostash-pop step; got:\n%s", s)
	}
	// MUST NOT remove the worktree (no silent cleanup on corruption).
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[1] == foo && c.Args[2] == "worktree" && slices.Contains(c.Args, "remove") {
			t.Fatalf("must not remove worktree on pop conflict; got %v", c.Args)
		}
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

// TestUpdateViaWorktree_WorktreeAddFails: when step-1 `worktree add` fails the
// run errors, but the worktree/branch are cleared on this path (nothing was
// created), so the summary must NOT name a left-behind worktree path for foo.
func TestUpdateViaWorktree_WorktreeAddFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error when worktree add fails")
	}
	// worktree/branch cleared → summary must not point users at a phantom path.
	if strings.Contains(out.String(), wt) {
		t.Errorf("summary should not name a left-behind worktree path on worktree-add failure; got:\n%s", out.String())
	}
}

// TestUpdateViaWorktree_PushFails: a failed step-7 push errors the run and never
// reaches the integration steps (merge --ff-only / worktree remove).
func TestUpdateViaWorktree_PushFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	// Steps 1–6 succeed; push fails.
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "push", "origin", "HEAD:main"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error when push fails")
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && slices.Contains(c.Args, "merge") {
			t.Errorf("merge --ff-only must not run after push failure; got %v", c.Args)
		}
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[2] == "worktree" && slices.Contains(c.Args, "remove") {
			t.Errorf("worktree remove must not run after push failure; got %v", c.Args)
		}
	}
}

// TestUpdateViaWorktree_WorktreeRemoveFailIsOkWithResidue: integration succeeds
// (merge --ff-only) but step-8 `worktree remove` fails. The outcome is "ok"
// (integration landed) yet the summary must surface the cleanup hint so the
// left-behind worktree is discoverable. `branch -d` is not reached on this path.
func TestUpdateViaWorktree_WorktreeRemoveFailIsOkWithResidue(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("main\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "merge", "--ff-only", branch}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "worktree", "remove", wt},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	// Integration landed → run succeeds (no error) even though cleanup failed.
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err != nil {
		t.Fatalf("worktree-remove failure should still be ok; got %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "✓ foo") {
		t.Errorf("summary should mark foo ok; got:\n%s", s)
	}
	if !strings.Contains(s, "prune") {
		t.Errorf("summary should surface the cleanup hint (prune) on residue; got:\n%s", s)
	}
}

// assertLeftBehind asserts a failed/deferred per-repo run: the summary names the
// stopping `step` and the left-behind worktree path, and step 8's `worktree
// remove` was never reached (leave-on-failure).
func assertLeftBehind(t *testing.T, f *exec.FakeRunner, summary, foo, wt, step string) {
	t.Helper()
	if !strings.Contains(summary, step) {
		t.Errorf("summary should name stopping step %q; got:\n%s", step, summary)
	}
	if !strings.Contains(summary, wt) {
		t.Errorf("summary should name left-behind worktree %q; got:\n%s", wt, summary)
	}
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[1] == foo && c.Args[2] == "worktree" && slices.Contains(c.Args, "remove") {
			t.Errorf("must not remove worktree on failure/defer; got %v", c.Args)
		}
	}
}

// TestUpdateViaWorktree_FetchOriginFails: step-2 `git -C <wt> fetch origin`
// failure errors the run at "fetch-origin", before relock, leaving the worktree.
func TestUpdateViaWorktree_FetchOriginFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error when step-2 fetch origin fails")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "fetch-origin")
}

// TestUpdateViaWorktree_UpdateLocksFails: step-3 `./update-locks.sh` failure
// errors the run at "update-locks" and leaves the worktree behind.
func TestUpdateViaWorktree_UpdateLocksFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil,
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "./update-locks.sh", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error when update-locks fails")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "update-locks")
}

// TestUpdateViaWorktree_RebaseLocalMainConflictAborts: step-4 `git -C <wt>
// rebase main` conflict errors at "rebase-local-main" and must run `rebase
// --abort` before leaving the worktree behind.
func TestUpdateViaWorktree_RebaseLocalMainConflictAborts(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", wt, "rebase", "--abort"}, exec.Result{}, nil)

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error on step-4 rebase-local-main conflict")
	}
	if !calledWith(f, "git", []string{"-C", wt, "rebase", "--abort"}) {
		t.Fatalf("expected rebase --abort after step-4 conflict")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "rebase-local-main")
}

// TestUpdateViaWorktree_RefetchOriginFails: step-5's second `git -C <wt> fetch
// origin` (the re-fetch catching remote drift) failure errors at
// "refetch-origin". The two identical fetch-origin scripts are consumed FIFO:
// the first (step 2) succeeds, the second (step 5) fails.
func TestUpdateViaWorktree_RefetchOriginFails(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil) // step 2 OK
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil)
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, // step 5 re-fetch FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error when step-5 re-fetch origin fails")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "refetch-origin")
}

// TestUpdateViaWorktree_RebaseOriginMain2ConflictAborts: step-5's second rebase
// onto origin/main conflicts → errors at "rebase-origin-main-2" and aborts. The
// two identical `rebase origin/main` scripts are consumed FIFO: step 3 succeeds,
// step 5 fails.
func TestUpdateViaWorktree_RebaseOriginMain2ConflictAborts(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	f.AddResponse("git", []string{"-C", foo, "worktree", "add", "-b", branch, wt, "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, exec.Result{}, nil) // step 3 OK
	f.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "main"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "fetch", "origin"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", wt, "rebase", "origin/main"}, // step 5 rebase FAILS
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("git", []string{"-C", wt, "rebase", "--abort"}, exec.Result{}, nil)

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected error on step-5 rebase-origin-main-2 conflict")
	}
	if !calledWith(f, "git", []string{"-C", wt, "rebase", "--abort"}) {
		t.Fatalf("expected rebase --abort after step-5 conflict")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "rebase-origin-main-2")
}

// TestUpdateViaWorktree_OtherBranchRefFfDefers: the step-8 ref-only ff
// (`git -C <primary> fetch . <branch>:main`, taken when main is not checked out)
// fails → the run defers (errors). Because push (step 7) already advanced remote
// main, the worktree + branch are left for manual recovery. (The SUCCESS of this
// path is covered by TestUpdateViaWorktree_OtherBranchRefFf.)
func TestUpdateViaWorktree_OtherBranchRefFfDefers(t *testing.T) {
	updateRunStampFn = func() string { return "TEST" }
	branch := "pn-update/TEST"
	root, foo, wt, f := wtUpdateFixture(t)
	scriptThroughPush(f, foo, wt, branch)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "HEAD"}, exec.Result{Stdout: []byte("feature-x\n")}, nil)
	f.AddResponse("git", []string{"-C", foo, "fetch", ".", branch + ":main"},
		exec.Result{ExitCode: 1}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 1}})

	w, _ := Open(root, f)
	var out bytes.Buffer
	if err := w.Update(context.Background(), &out, UpdateOptions{ULLibDir: "/x"}); err == nil {
		t.Fatalf("expected deferred error when step-8 ref-ff fails on a feature branch")
	}
	assertLeftBehind(t, f, out.String(), foo, wt, "ff-ref")
}

func TestUpdateViaWorktree_RefusesInsideSet(t *testing.T) {
	// A coordinated set lives at <base>/.workforests/<branch>; rooting pn there must
	// refuse the worktree flow and point at --in-place. (.workforests is the default
	// workforests_dir name, so inWorkforest() detects it structurally.)
	base := t.TempDir()
	setRoot := filepath.Join(base, ".workforests", "feature-x")
	if err := os.MkdirAll(setRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(setRoot, "pn-workspace.toml"), "[workspace]\nterminal=\"foo\"\n[repos.foo]\nurl=\"github:o/foo\"\n")

	w, err := Open(setRoot, exec.NewFakeRunner())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{ULLibDir: "/x"}); err == nil || !strings.Contains(err.Error(), "--in-place") {
		t.Fatalf("expected refuse-in-set error mentioning --in-place, got %v", err)
	}

	// --in-place must still work inside a set (no guard there): script the in-place
	// no-upstream flow for the single repo.
	foo := filepath.Join(setRoot, "foo")
	f2 := exec.NewFakeRunner()
	f2.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f2.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f2.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f2.AddResponse("./update-locks.sh", nil, exec.Result{}, nil)
	f2.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)
	w2, err := Open(setRoot, f2)
	if err != nil {
		t.Fatalf("Open(2): %v", err)
	}
	if err := w2.Update(context.Background(), &bytes.Buffer{}, UpdateOptions{InPlace: true, ULLibDir: "/x"}); err != nil {
		t.Fatalf("--in-place inside set should work: %v", err)
	}
}
