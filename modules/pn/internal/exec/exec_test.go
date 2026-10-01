package exec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealRunner_StreamsLiveWhileCapturing(t *testing.T) {
	r := NewRealRunner()
	var live bytes.Buffer
	res, err := r.Run(context.Background(), "sh", []string{"-c", "echo out; echo err 1>&2"}, RunOptions{
		Stdout: &live,
		Stderr: &live,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Live sink receives both streams as the command runs.
	if !strings.Contains(live.String(), "out") || !strings.Contains(live.String(), "err") {
		t.Errorf("expected live sink to receive full output, got %q", live.String())
	}
	// Result still captures stdout/stderr for callers that parse them.
	if !strings.Contains(string(res.Stdout), "out") {
		t.Errorf("expected captured stdout, got %q", string(res.Stdout))
	}
	if !strings.Contains(string(res.Stderr), "err") {
		t.Errorf("expected captured stderr, got %q", string(res.Stderr))
	}
}

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

// cancelScript traps SIGTERM (recording it to $1), then signals readiness via
// $2 and waits. If $3 is "ignore" the trap does not exit, so only SIGKILL ends it.
const cancelScript = `
marker="$1"; ready="$2"; mode="$3"
if [ "$mode" = ignore ]; then
  trap 'echo term > "$marker"' TERM
else
  trap 'echo term > "$marker"; exit 0' TERM
fi
touch "$ready"
while :; do sleep 0.05; done
`

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestRealRunner_CancelDeliversSIGTERM(t *testing.T) {
	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "marker"), filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewRealRunner().Run(ctx, "sh", []string{"-c", cancelScript, "sh", marker, ready, "exit"}, RunOptions{})
		done <- err
	}()
	waitForFile(t, ready)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("child did not observe SIGTERM: %v", err)
	}
}

func TestRealRunner_SIGTERMIgnoringChildKilledAfterWaitDelay(t *testing.T) {
	old := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = old })

	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "marker"), filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewRealRunner().Run(ctx, "sh", []string{"-c", cancelScript, "sh", marker, ready, "ignore"}, RunOptions{})
		done <- err
	}()
	waitForFile(t, ready)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from killed child")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM-ignoring child was never SIGKILLed")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("returned in %v, before WaitDelay elapsed", elapsed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("child should have received SIGTERM first: %v", err)
	}
}
