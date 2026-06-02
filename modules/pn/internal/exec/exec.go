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
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
	// Stdout, if set, receives the command's standard output live as it runs,
	// in addition to being captured in Result.Stdout. Use for long-running
	// commands (build/apply) so their full output reaches the terminal.
	Stdout io.Writer
	// Stderr, if set, receives the command's standard error live as it runs,
	// in addition to being captured in Result.Stderr.
	Stderr io.Writer
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
	// Capture into buffers (for callers that parse output) and, when a live
	// sink is provided, tee to it so the full output reaches the terminal as
	// the command runs rather than being withheld until completion.
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if opts.Stdout != nil {
		cmd.Stdout = io.MultiWriter(&stdout, opts.Stdout)
	}
	if opts.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, opts.Stderr)
	}
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
	stderr := strings.TrimSpace(string(e.Result.Stderr))
	if len(stderr) > 512 {
		stderr = stderr[:512] + "... (truncated)"
	}
	if stderr == "" {
		return fmt.Sprintf("%s exited %d", e.Name, e.Result.ExitCode)
	}
	return fmt.Sprintf("%s exited %d: %s", e.Name, e.Result.ExitCode, stderr)
}
