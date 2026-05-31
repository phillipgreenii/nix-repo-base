package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestResolveHookPath_Absolute(t *testing.T) {
	got, err := resolveHookPath("/usr/bin/echo", "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/usr/bin/echo" {
		t.Errorf("got %q want /usr/bin/echo", got)
	}
}

func TestResolveHookPath_FileRelative(t *testing.T) {
	tmp := t.TempDir()
	got, err := resolveHookPath("./hooks/foo.sh", tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(tmp, "hooks/foo.sh")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestResolveHookPath_PATHRelative(t *testing.T) {
	got, err := resolveHookPath("pn-osx-tcc-check", "/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "pn-osx-tcc-check" {
		t.Errorf("got %q (PATH-relative names returned as-is)", got)
	}
}

func TestRunHooks_OrderedExecution(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "first"}, exec.Result{}, nil)
	f.AddResponse("sh", []string{"-c", "second"}, exec.Result{}, nil)

	err := RunHooks(context.Background(), f, []string{"first", "second"}, "/workspace", HookPhasePre)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if !strings.Contains(strings.Join(calls[0].Args, " "), "first") {
		t.Errorf("first call should be 'first', got %v", calls[0].Args)
	}
	if !strings.Contains(strings.Join(calls[1].Args, " "), "second") {
		t.Errorf("second call should be 'second', got %v", calls[1].Args)
	}
}

func TestRunHooks_PreFailureAborts(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "boom"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("sh", []string{"-c", "should-not-run"}, exec.Result{}, nil)

	err := RunHooks(context.Background(), f, []string{"boom", "should-not-run"}, "/workspace", HookPhasePre)
	if err == nil {
		t.Fatal("expected error from pre-hook failure; got nil")
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Errorf("expected execution to stop at first failure (1 call), got %d", len(calls))
	}
}

func TestRunHooks_PostFailureWarnsButDoesNotAbort(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "boom"}, exec.Result{ExitCode: 1}, &exec.CommandError{Name: "sh", Result: exec.Result{ExitCode: 1}})
	f.AddResponse("sh", []string{"-c", "after"}, exec.Result{}, nil)

	err := RunHooks(context.Background(), f, []string{"boom", "after"}, "/workspace", HookPhasePost)
	if err != nil {
		t.Fatalf("post failures should not return errors; got %v", err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Errorf("expected both post hooks to run, got %d calls", len(calls))
	}
}

func TestRunHooks_EmptyList(t *testing.T) {
	f := exec.NewFakeRunner()
	if err := RunHooks(context.Background(), f, nil, "/workspace", HookPhasePre); err != nil {
		t.Errorf("empty hook list should be a no-op, got %v", err)
	}
}
