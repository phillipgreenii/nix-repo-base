package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// whatever fn wrote to it. Mirrors the pattern already used in
// edges_test.go for the same purpose.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

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

// TestRunHooks_PostSuccessAcknowledged closes the tc-5cfwb reproduction: a
// post-hook that ran and succeeded must leave evidence in pn's output, so it
// is distinguishable from one that never fired at all.
func TestRunHooks_PostSuccessAcknowledged(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "echo mirrors-synced"}, exec.Result{Stdout: []byte("mirrors-synced\n")}, nil)

	var runErr error
	out := captureStderr(t, func() {
		runErr = RunHooks(context.Background(), f, []string{"echo mirrors-synced"}, "/workspace", HookPhasePost)
	})
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if !strings.Contains(out, "echo mirrors-synced") {
		t.Errorf("expected a one-line acknowledgement naming the hook; got stderr: %q", out)
	}
	// The acknowledgement must NOT relay the hook's own stdout body — only
	// prove that it ran (see the decision in RunHooks' doc comment).
	if strings.Contains(out, "mirrors-synced") && !strings.Contains(out, "echo mirrors-synced") {
		t.Errorf("acknowledgement appears to relay hook stdout body, not just name it: %q", out)
	}
}

// TestRunHooks_PreSuccessStaysSilent: pre-hooks are gates, and a gate passing
// silently is conventional (bd tc-5cfwb) — success must not print anything.
func TestRunHooks_PreSuccessStaysSilent(t *testing.T) {
	f := exec.NewFakeRunner()
	f.AddResponse("sh", []string{"-c", "echo gate-passed"}, exec.Result{Stdout: []byte("gate-passed\n")}, nil)

	var runErr error
	out := captureStderr(t, func() {
		runErr = RunHooks(context.Background(), f, []string{"echo gate-passed"}, "/workspace", HookPhasePre)
	})
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if out != "" {
		t.Errorf("expected pre-hook success to stay silent; got stderr: %q", out)
	}
}

func TestRunHooks_EmptyList(t *testing.T) {
	f := exec.NewFakeRunner()
	if err := RunHooks(context.Background(), f, nil, "/workspace", HookPhasePre); err != nil {
		t.Errorf("empty hook list should be a no-op, got %v", err)
	}
}
