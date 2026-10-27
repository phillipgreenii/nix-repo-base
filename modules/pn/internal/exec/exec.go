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
	"sync"
	"syscall"
	"time"
)

// killGrace is how long a child gets to exit after SIGTERM (sent on context
// cancel) before os/exec escalates to SIGKILL. A package var so tests can
// shorten it.
var killGrace = 10 * time.Second

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
	// WrapNix marks a long-running nix invocation (nix build/flake check/fmt/
	// run, darwin-rebuild, nixos-rebuild) that the Runner MAY run through the
	// pg-nix-log-wrapped telemetry wrapper (ADR 0028). It is set ONLY by the
	// long-running call sites; short stdout-parsed probes (nix eval/flake
	// metadata/path-info), `sudo nix-store --gc` and the like MUST leave it
	// false, because the decorator never rewrites by command name. It changes
	// nothing for a Runner that is not the production decorator stack.
	WrapNix bool
	// PropagateTrace marks a call whose child (a user hook or update-locks.sh)
	// may run nix itself: when telemetry is on the Runner exports TRACEPARENT,
	// taken from the pn.exec span of this call, in the child's environment so
	// that nested nix shows under the pn.verb trace (ADR 0028). argv is never
	// rewritten, and with telemetry off the environment is left exactly as
	// before. Like WrapNix it is set only by explicit call sites and changes
	// nothing for a Runner that is not the production decorator stack.
	PropagateTrace bool
}

// Result captures the outcome of a Run call.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// Signal names the signal that killed the process (e.g. "SIGKILL") when it
	// died on one, in which case ExitCode is -1; empty otherwise.
	Signal string
}

// syncWriter serializes concurrent Writes to an underlying io.Writer. See
// the comment in Run for why this is needed.
type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// realRunner wraps os/exec. grace, when non-zero, overrides the package-level
// killGrace for this runner (a test seam; production leaves it zero).
type realRunner struct{ grace time.Duration }

func (r *realRunner) killGrace() time.Duration {
	if r.grace > 0 {
		return r.grace
	}
	return killGrace
}

// NewRealRunner returns a Runner backed by os/exec, wrapped in the pn.exec
// tracing decorator and, inside it, the TRACEPARENT environment decorator and
// the nix wrapper decorator (all no-ops unless telemetry is enabled in the
// context). Every production caller goes through here, so every subprocess pn
// spawns via the Runner is spanned, the long-running nix calls
// (RunOptions.WrapNix) run through the wrapper as children of their pn.exec
// span, and hooks and update-locks.sh (RunOptions.PropagateTrace) get that
// span as TRACEPARENT.
func NewRealRunner() Runner {
	return newRealRunner(0)
}

// NewRealRunnerWithKillGrace is NewRealRunner with a caller-chosen WaitDelay /
// SIGTERM-to-SIGKILL grace instead of the production 10s. It exists so tests
// in other packages can exercise the real os/exec path (a descendant holding
// the output pipe open past the grace) without sleeping 10s. Production code
// MUST use NewRealRunner.
func NewRealRunnerWithKillGrace(d time.Duration) Runner {
	return newRealRunner(d)
}

func newRealRunner(grace time.Duration) Runner {
	return WithTracing(WithTraceEnv(WithNixWrapper(&realRunner{grace: grace})))
}

func (r *realRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// os/exec resolves a bare name against the PARENT's PATH, ignoring
	// cmd.Env. When the caller overrides PATH (ResolveTool's fallback), resolve
	// the name against that PATH instead, so the lookup and the child agree.
	if p, ok := opts.Env["PATH"]; ok && cmd.Err != nil && !strings.ContainsRune(name, os.PathSeparator) {
		if path, err := lookPathIn(name, p); err == nil {
			cmd.Path, cmd.Err = path, nil
		}
	}
	// On cancel, ask the child to terminate (SIGTERM) so it can clean up and
	// flush; only if it is still running after WaitDelay does os/exec SIGKILL
	// it. The default (immediate SIGKILL) gives children no chance to exit
	// cleanly.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	grace := r.killGrace()
	cmd.WaitDelay = grace
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
	if opts.Stdout != nil || opts.Stderr != nil {
		// os/exec runs the stdout and stderr copy loops on separate
		// goroutines whenever cmd.Stdout and cmd.Stderr are not the exact
		// same Writer value (which they never are here, since each gets
		// wrapped in its own io.MultiWriter below). Callers routinely pass
		// the SAME writer as both RunOptions.Stdout and RunOptions.Stderr
		// (e.g. to combine a subprocess's stdout and stderr into one
		// terminal/log sink) — most io.Writer implementations, including
		// *bytes.Buffer, are not safe for concurrent use, so without this
		// mutex that pattern races.
		var liveMu sync.Mutex
		if opts.Stdout != nil {
			cmd.Stdout = io.MultiWriter(&stdout, &syncWriter{mu: &liveMu, w: opts.Stdout})
		}
		if opts.Stderr != nil {
			cmd.Stderr = io.MultiWriter(&stderr, &syncWriter{mu: &liveMu, w: opts.Stderr})
		}
	}
	err := cmd.Run()
	if waitDelayAfterSuccess(ctx, cmd, err) {
		// The command itself exited 0, but a descendant it spawned (e.g. a
		// backgrounded `timeout 30s launchctl kickstart -k`) still held the
		// stdout/stderr pipe open when WaitDelay expired, so os/exec gave up
		// waiting for I/O and returned ErrWaitDelay. That is not a command
		// failure: treat it as success (pg2-gb5ng) but say so loudly, because
		// the captured output may be missing whatever the descendant wrote
		// after the pipes were closed.
		warnTo := opts.Stderr
		if warnTo == nil {
			warnTo = os.Stderr
		}
		_, _ = fmt.Fprintf(warnTo, "warning: %s exited successfully, but a background process kept its output open for more than %s; "+
			"treating the command as succeeded (captured output may be incomplete)\n", name, grace)
		err = nil
	}
	res := Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = signalName(ws.Signal())
		}
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

// waitDelayAfterSuccess reports whether err is os/exec's ErrWaitDelay for a
// command that nonetheless exited successfully on its own: the process state
// says success and the context was not cancelled. A non-zero exit, a signal
// death, a SIGTERM/SIGKILL delivered by context cancel, and any other error
// all return false and keep failing exactly as before.
func waitDelayAfterSuccess(ctx context.Context, cmd *exec.Cmd, err error) bool {
	return errors.Is(err, exec.ErrWaitDelay) &&
		ctx.Err() == nil &&
		cmd.ProcessState != nil &&
		cmd.ProcessState.Success()
}

// CommandError is returned when a command exits non-zero.
type CommandError struct {
	Name   string
	Args   []string
	Result Result
}

// ExitSignal names the signal that killed the command, or "" when it exited on
// its own. The telemetry package reads it (by interface) to mark a pn.exec span
// error.type=signal.
func (e *CommandError) ExitSignal() string { return e.Result.Signal }

// signalName renders a signal as its SIG name for the common signals and as
// "SIG<n>" for the rest.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return fmt.Sprintf("SIG%d", int(sig))
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
