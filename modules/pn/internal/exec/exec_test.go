package exec

import (
	"context"
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
	got := strings.TrimSpace(string(res.Stdout))
	if got != dir {
		t.Errorf("expected pwd=%q, got %q", dir, got)
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
