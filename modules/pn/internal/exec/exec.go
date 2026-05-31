// Package exec provides a Runner interface for invoking external commands.
//
// The interface exists so tests can inject a FakeRunner that records calls
// and returns scripted results, removing the need for heavy mocking
// frameworks.
package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

// Runner runs external commands.
type Runner interface {
	Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error)
}

// RunOptions configures a single Run call.
type RunOptions struct {
	// Dir sets the working directory. Empty = inherit caller's cwd.
	Dir string
	// Env adds (or overrides) environment variables. Nil = inherit caller's env.
	Env map[string]string
	// Stdin provides standard input. Nil = empty.
	Stdin io.Reader
}

// Result captures the outcome of a Run call.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// realRunner wraps os/exec.
type realRunner struct{}

// NewRealRunner returns a Runner backed by os/exec.
func NewRealRunner() Runner {
	return &realRunner{}
}

func (r *realRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = os.Environ()
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
	} else if err == nil {
		res.ExitCode = 0
	} else {
		// Subprocess didn't start (binary not found, etc.); ExitCode stays 0
		// but err is non-nil so the caller knows.
		return res, err
	}
	if res.ExitCode != 0 {
		return res, &CommandError{Name: name, Args: args, Result: res}
	}
	return res, nil
}

// CommandError is returned when a command exits non-zero.
type CommandError struct {
	Name   string
	Args   []string
	Result Result
}

func (e *CommandError) Error() string {
	return e.Name + " exited " + itoa(e.Result.ExitCode)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
