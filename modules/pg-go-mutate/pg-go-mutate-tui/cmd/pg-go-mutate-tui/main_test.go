package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binPath is the compiled test binary shared by every test in this file.
//
// NOTE: this deliberately does NOT drive the CLI via `go run .` as the plan's
// literal transcription did. `go run` does not propagate the child process's
// real exit code to its own caller — on ANY non-zero exit it always exits 1
// itself (it only *prints* "exit status N" as text; verified empirically
// against this repo's toolchain: `go run` wrapping a program that returns 2
// reports back exec.ExitError.ExitCode() == 1, never 2). That makes
// TestMissingRootExitsTwo unwinnable through `go run` no matter what main.go
// returns, so the test is rewritten to build the real binary once and exec it
// directly, which propagates exit codes correctly.
var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "pg-go-mutate-tui-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmpDir, "pg-go-mutate-tui")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: build failed:", err, string(out))
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

func TestHelpExitsZero(t *testing.T) {
	cmd := exec.Command(binPath, "--help")
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help should exit 0, got: %v", err)
	}
}

func TestMissingRootExitsTwo(t *testing.T) {
	cmd := exec.Command(binPath)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("missing --root should exit 2, got: %v", err)
	}
}
