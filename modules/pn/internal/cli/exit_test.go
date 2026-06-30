// internal/cli/exit_test.go
package cli

import (
	"errors"
	"testing"
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
