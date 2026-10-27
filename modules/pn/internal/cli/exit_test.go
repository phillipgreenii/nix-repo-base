// internal/cli/exit_test.go
package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/workspace"
)

func TestExitCode(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("nil err: want 0, got %d", got)
	}
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Fatalf("plain err: want 1, got %d", got)
	}
	if got := ExitCode(ExitCodeError{Code: 2}); got != 2 {
		t.Fatalf("ExitCodeError{2}: want 2, got %d", got)
	}
	if got := (ExitCodeError{Code: 2}).Error(); got == "" {
		t.Fatal("ExitCodeError.Error() must be non-empty")
	}
}

// TestExitCode_PropagatesCommandError verifies a failed subprocess's own
// exit code is surfaced rather than collapsed to 1 (bead pg2-x3r0a), both when
// the CommandError is returned directly and when it is wrapped with %w.
func TestExitCode_PropagatesCommandError(t *testing.T) {
	ce := &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 3}}
	if got := ExitCode(ce); got != 3 {
		t.Errorf("direct CommandError: want 3, got %d", got)
	}
	if got := ExitCode(fmt.Errorf("build failed: %w", ce)); got != 3 {
		t.Errorf("wrapped CommandError: want 3, got %d", got)
	}
	// A CommandError with a zero exit code (e.g. the binary never started) is
	// not a clean subprocess exit code, so it falls back to 1.
	zero := &exec.CommandError{Name: "nix", Result: exec.Result{ExitCode: 0}}
	if got := ExitCode(zero); got != 1 {
		t.Errorf("zero-code CommandError: want 1, got %d", got)
	}
	// ExitCodeError still wins when both are in play.
	if got := ExitCode(ExitCodeError{Code: 2}); got != 2 {
		t.Errorf("ExitCodeError precedence: want 2, got %d", got)
	}
}

// TestExitCode_ToolNotFoundIsDistinct: a tool pn could not find before launching
// anything maps to its own status (bead pg2-sdbhh4.12), also when wrapped.
func TestExitCode_ToolNotFoundIsDistinct(t *testing.T) {
	err := &exec.ToolNotFoundError{Tool: "darwin-rebuild", Tried: exec.SystemProfileBinDirs}
	if got := ExitCode(err); got != exec.ToolNotFoundExitCode {
		t.Fatalf("want %d, got %d", exec.ToolNotFoundExitCode, got)
	}
	if got := ExitCode(fmt.Errorf("build: %w", err)); got != exec.ToolNotFoundExitCode {
		t.Fatalf("wrapped: want %d, got %d", exec.ToolNotFoundExitCode, got)
	}
}

// TestExitCode_LockBusyIsDistinct: a build/apply refused by the single-flight
// lock exits with its own status (bead pg2-sdbhh4.11), also when wrapped, and
// that status collides with none of pn's other fixed statuses.
func TestExitCode_LockBusyIsDistinct(t *testing.T) {
	err := &workspace.LockBusyError{Key: "apply", Path: "/x/apply-leaf-h.lock"}
	if got := ExitCode(err); got != workspace.LockBusyExitCode {
		t.Fatalf("want %d, got %d", workspace.LockBusyExitCode, got)
	}
	if got := ExitCode(fmt.Errorf("apply: %w", err)); got != workspace.LockBusyExitCode {
		t.Fatalf("wrapped: want %d, got %d", workspace.LockBusyExitCode, got)
	}
	for _, other := range []int{0, 1, 2, exec.ToolNotFoundExitCode, 126, 127, 130, 143} {
		if workspace.LockBusyExitCode == other {
			t.Errorf("LockBusyExitCode collides with %d", other)
		}
	}
}
