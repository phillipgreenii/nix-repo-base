package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

func newTestWorkspace(t *testing.T, runner exec.Runner, tomlBody string) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, workspace.ConfigFileName), []byte(tomlBody), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	w, err := workspace.Open(dir, runner)
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

func TestRunWithHooks_NoHooksPassthrough(t *testing.T) {
	f := exec.NewFakeRunner()
	w := newTestWorkspace(t, f, `
[workspace]
name = "test"
`)
	called := false
	err := runWithHooks(context.Background(), w, "update", func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("verb was not invoked")
	}
	if got := len(f.Calls()); got != 0 {
		t.Errorf("expected no runner calls, got %d", got)
	}
}

func TestRunWithHooks_PreFailureAbortsVerbAndPost(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "boom"}, exec.Result{ExitCode: 1},
		&exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 1}})
	w := newTestWorkspace(t, f, `
[workspace]
name = "test"

[[hooks]]
when = ["pre-update"]
run = ["boom"]

[[hooks]]
when = ["post-update"]
run = ["should-not-run"]
`)
	called := false
	err := runWithHooks(context.Background(), w, "update", func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected pre-hook failure to propagate")
	}
	if called {
		t.Error("verb must not run when pre-hook fails")
	}
	if got := len(f.Calls()); got != 1 {
		t.Errorf("only the failing pre-hook should run; got %d calls", got)
	}
}

func TestRunWithHooks_PostFailureSwallowed(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "boom"}, exec.Result{ExitCode: 1},
		&exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 1}})
	w := newTestWorkspace(t, f, `
[workspace]
name = "test"

[[hooks]]
when = ["post-update"]
run = ["boom"]
`)
	err := runWithHooks(context.Background(), w, "update", func() error { return nil })
	if err != nil {
		t.Fatalf("post-hook failure must not propagate; got %v", err)
	}
	if got := len(f.Calls()); got != 1 {
		t.Errorf("expected the failing post-hook to run; got %d calls", got)
	}
}

func TestRunWithHooks_VerbErrorPropagatesAndPostStillRuns(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "after"}, exec.Result{}, nil)
	w := newTestWorkspace(t, f, `
[workspace]
name = "test"

[[hooks]]
when = ["post-update"]
run = ["after"]
`)
	verbErr := errors.New("verb failed")
	err := runWithHooks(context.Background(), w, "update", func() error { return verbErr })
	if !errors.Is(err, verbErr) {
		t.Errorf("expected verb error to propagate; got %v", err)
	}
	if got := len(f.Calls()); got != 1 {
		t.Errorf("post hook must still run after verb error; got %d calls", got)
	}
}

func TestRunWithHooks_PreThenVerbThenPostOrdering(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "pre-1"}, exec.Result{}, nil)
	f.AddResponse("sh", []string{"-c", "pre-2"}, exec.Result{}, nil)
	f.AddResponse("sh", []string{"-c", "post-1"}, exec.Result{}, nil)
	w := newTestWorkspace(t, f, `
[workspace]
name = "test"

[[hooks]]
when = ["pre-update"]
run = ["pre-1", "pre-2"]

[[hooks]]
when = ["post-update"]
run = ["post-1"]
`)
	var verbAt int
	err := runWithHooks(context.Background(), w, "update", func() error {
		verbAt = len(f.Calls())
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verbAt != 2 {
		t.Errorf("verb should run after the 2 pre-hooks; ran after %d calls", verbAt)
	}
	if got := len(f.Calls()); got != 3 {
		t.Errorf("expected 3 hook calls total (2 pre + 1 post); got %d", got)
	}
}
