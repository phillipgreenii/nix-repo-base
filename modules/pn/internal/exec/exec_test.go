package exec

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealRunner_RunsCommand(t *testing.T) {
	r := NewRealRunner()
	res, err := r.Run(context.Background(), "echo", []string{"hello"}, RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("expected stdout to contain 'hello', got %q", string(res.Stdout))
	}
}

func TestRealRunner_CapturesExitCode(t *testing.T) {
	r := NewRealRunner()
	res, err := r.Run(context.Background(), "sh", []string{"-c", "exit 7"}, RunOptions{})
	if err == nil {
		t.Fatal("expected error from non-zero exit; got nil")
	}
	if res.ExitCode != 7 {
		t.Errorf("expected exit code 7, got %d", res.ExitCode)
	}
}

func TestRealRunner_RespectsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	r := NewRealRunner()
	res, err := r.Run(context.Background(), "pwd", nil, RunOptions{Dir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Compare with symlinks resolved: on macOS t.TempDir() yields /tmp/... but
	// pwd reports the canonical /private/tmp/... form. Resolving both sides
	// makes the assertion robust to any tmp-directory layout.
	got := strings.TrimSpace(string(res.Stdout))
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(got %q): %v", got, err)
	}
	wantResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(dir %q): %v", dir, err)
	}
	if gotResolved != wantResolved {
		t.Errorf("expected pwd %q (resolved %q), got %q (resolved %q)", dir, wantResolved, got, gotResolved)
	}
}

func TestRealRunner_RespectsExtraEnv(t *testing.T) {
	r := NewRealRunner()
	res, err := r.Run(context.Background(), "sh", []string{"-c", "echo $MY_VAR"}, RunOptions{
		Env: map[string]string{"MY_VAR": "hello-from-test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(res.Stdout), "hello-from-test") {
		t.Errorf("expected MY_VAR in output, got %q", string(res.Stdout))
	}
}

func TestCommandError_IncludesStderr(t *testing.T) {
	r := NewRealRunner()
	_, err := r.Run(context.Background(), "sh", []string{"-c", "echo nope >&2; exit 2"}, RunOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error to include stderr 'nope', got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "exited 2") {
		t.Errorf("expected error to mention exit code 2, got %q", err.Error())
	}
}
