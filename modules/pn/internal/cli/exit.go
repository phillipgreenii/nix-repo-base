package cli

import (
	"errors"
	"fmt"
)

// ExitCodeError carries a process exit code up to main(). cobra only
// propagates an error, not a code, so commands that need a specific
// non-1 exit (e.g. doctor's 0/1/2) return this.
type ExitCodeError struct {
	Code int
	// Msg, when non-empty, is printed to stderr by main(); usually empty
	// because the command already rendered its own output.
	Msg string
}

func (e ExitCodeError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

// ExitCode maps an error to a process exit code: 0 for nil, the carried
// code for an ExitCodeError, 1 for any other error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec ExitCodeError
	if errors.As(err, &ec) {
		return ec.Code
	}
	return 1
}
