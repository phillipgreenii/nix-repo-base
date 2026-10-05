package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

// wrappedCommands are the only argv[0] values the nix wrapper decorator
// rewrites (directly, or directly after sudo). The set is deliberately exact:
// pn runs sudo and nix in many other forms (sudo nix-store --gc, sudo -E ...,
// sh -c build commands) that must keep running unwrapped.
var wrappedCommands = map[string]bool{
	"darwin-rebuild": true,
	"nixos-rebuild":  true,
	"nix":            true,
}

// wrapRunner is a decorator that runs the long-running nix calls
// (RunOptions.WrapNix) through pg-nix-log-wrapped so their nix activity becomes
// nix.invocation / nix.build spans under the caller's pn.exec span (ADR 0028).
// It sits INSIDE the tracing decorator so the span context in ctx is the
// pn.exec span. It FAILS OPEN: whenever telemetry is off, the call is not
// marked WrapNix, the command has a form it does not recognise, or the wrapper
// cannot be used, it delegates the original name and args untouched.
//
// The rewrite only prepends to argv; the wrapped command's own arguments
// (template args plus the --override-input pairs pn appends) are passed after a
// "--" verbatim.
type wrapRunner struct {
	inner Runner
	// evalSymlinks resolves the wrapper under sudo (filepath.EvalSymlinks in
	// production).
	evalSymlinks func(string) (string, error)
	// usable reports whether path is an executable regular file (stat in
	// production).
	usable func(string) bool
	// getenv is os.Getenv in production.
	getenv func(string) string
}

// WithNixWrapper wraps inner in the nix wrapper decorator. It reads the
// resolved telemetry configuration from the context (telemetrycfg.RunState and
// the Telemetry), so it needs no configuration of its own.
func WithNixWrapper(inner Runner) Runner {
	return &wrapRunner{inner: inner, evalSymlinks: filepath.EvalSymlinks, usable: isExecutableFile, getenv: os.Getenv}
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Run implements Runner.
func (r *wrapRunner) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	wname, wargs, ok := r.rewrite(ctx, name, args, opts)
	if !ok {
		return r.inner.Run(ctx, name, args, opts)
	}
	res, err := r.inner.Run(ctx, wname, wargs, opts)
	// Errors must read as if the wrapper were absent: report the command pn
	// asked for, not the wrapper argv.
	var ce *CommandError
	if errors.As(err, &ce) {
		ce.Name, ce.Args = name, args
	}
	return res, err
}

// rewrite computes the wrapped command, or ok=false to run unwrapped.
func (r *wrapRunner) rewrite(ctx context.Context, name string, args []string, opts RunOptions) (string, []string, bool) {
	if !opts.WrapNix {
		return "", nil, false
	}
	st, ok := wrapState(ctx)
	if !ok {
		return "", nil, false
	}
	under, sudo := wrapTarget(name, args)
	if under == "" {
		return "", nil, false
	}

	wrapper := st.Res.WrapperPath
	if sudo {
		real, err := telemetrycfg.ValidateSudoWrapper(wrapper, r.evalSymlinks)
		if err != nil {
			return "", nil, false
		}
		wrapper = real
	} else if !filepath.IsAbs(wrapper) {
		return "", nil, false
	}
	if !r.usable(wrapper) {
		return "", nil, false
	}

	flags := make([]string, 0, 7)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		flags = append(flags, "--traceparent", fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags()))
	}
	flags = append(flags, "--otlp-endpoint", st.Res.Endpoint)
	if !sudo {
		// A root wrapper ignores --log-dir and always logs to its own
		// root-owned directory (ADR 0030), so pass it only to a user wrapper.
		if dir := telemetrycfg.NixLogDir(r.getenv); dir != "" {
			flags = append(flags, "--log-dir", dir)
		}
	}
	flags = append(flags, "--")

	if sudo {
		// name is "sudo"; args[0] is the wrapped command. Keep sudo, put the
		// wrapper (by absolute real path) between sudo and the command.
		out := make([]string, 0, 1+len(flags)+len(args))
		out = append(out, wrapper)
		out = append(out, flags...)
		out = append(out, args...)
		return name, out, true
	}
	out := make([]string, 0, len(flags)+1+len(args))
	out = append(out, flags...)
	out = append(out, name)
	out = append(out, args...)
	return wrapper, out, true
}

// wrapState returns the run's telemetry state when nix wrapping is configured
// at all: telemetry enabled, an endpoint resolved and a wrapper path named.
func wrapState(ctx context.Context) (*telemetrycfg.RunState, bool) {
	st := telemetrycfg.RunStateFrom(ctx)
	if st == nil || !st.Res.Enabled || st.Res.Endpoint == "" || st.Res.WrapperPath == "" || !telemetry.From(ctx).Enabled() {
		return nil, false
	}
	return st, true
}

// userWrapper returns the absolute path of the configured wrapper and the
// resolved OTLP endpoint for a NON-root child (never under sudo), or ok=false
// when the wrapper must not be used: telemetry off, no endpoint or wrapper
// path, a relative path, or a file that is not an executable regular file.
func userWrapper(ctx context.Context, usable func(string) bool) (path, endpoint string, ok bool) {
	st, ok := wrapState(ctx)
	if !ok || !filepath.IsAbs(st.Res.WrapperPath) || !usable(st.Res.WrapperPath) {
		return "", "", false
	}
	return st.Res.WrapperPath, st.Res.Endpoint, true
}

// NixWrapperArgv returns the argv prefix that runs a nix command under
// pg-nix-log-wrapped for a non-root child, up to and including "--":
//
//	<wrapper> --otlp-endpoint URL [--log-dir DIR] --
//
// It carries no --traceparent: the child (a hook or update-locks.sh) gets
// TRACEPARENT from the environment (WithTraceEnv), which the wrapper honours.
// It returns nil whenever the wrapper must not be used, so a caller that
// prepends it is byte-identical to before with telemetry off or the wrapper
// absent (ADR 0028).
func NixWrapperArgv(ctx context.Context) []string {
	path, endpoint, ok := userWrapper(ctx, isExecutableFile)
	if !ok {
		return nil
	}
	argv := []string{path, "--otlp-endpoint", endpoint}
	if dir := telemetrycfg.NixLogDir(os.Getenv); dir != "" {
		argv = append(argv, "--log-dir", dir)
	}
	return append(argv, "--")
}

// wrapTarget reports which wrapped command a call runs and whether it is run
// through sudo. It returns "" for every form that must run unwrapped. The sudo
// form is exactly sudo followed DIRECTLY by darwin-rebuild, nixos-rebuild or
// nix: any sudo option (-E, -u x, -n, ...), env, or another command is not
// recognised.
func wrapTarget(name string, args []string) (under string, sudo bool) {
	if name == "sudo" {
		if len(args) > 0 && wrappedCommands[args[0]] {
			return args[0], true
		}
		return "", false
	}
	if wrappedCommands[name] {
		return name, false
	}
	return "", false
}
