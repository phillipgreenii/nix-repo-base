package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

// recRunner records the single call it receives and returns a scripted outcome.
type recRunner struct {
	calls []Call
	res   Result
	err   error
}

func (r *recRunner) Run(_ context.Context, name string, args []string, opts RunOptions) (Result, error) {
	r.calls = append(r.calls, Call{Name: name, Args: append([]string(nil), args...), Opts: opts})
	return r.res, r.err
}

const (
	testEndpoint = "http://127.0.0.1:4318"
	storeWrapper = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-pg-nix-log-wrapped-1/bin/pg-nix-log-wrapped"
)

// wrapEnv bundles a wrapRunner whose filesystem probes are fakes, the recording
// inner runner, and a context carrying enabled telemetry.
type wrapEnv struct {
	r     *wrapRunner
	inner *recRunner
	ctx   context.Context
	rec   *telemetrytest.Recorder
}

// newWrapEnv returns an environment where telemetry is on, endpoint is
// testEndpoint, and wrapper is the configured wrapper_path. Every path is
// "executable"; evalSymlinks is the identity unless a test overrides it.
func newWrapEnv(t *testing.T, wrapper string) *wrapEnv {
	t.Helper()
	tel, rec := telemetrytest.New()
	res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, Source: telemetrycfg.SourceFlag, WrapperPath: wrapper}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx = telemetrycfg.WithRunState(ctx, telemetrycfg.NewRunState(res))
	ctx, end := telemetry.StartExec(ctx, "test") // a valid span context for --traceparent
	t.Cleanup(func() { end(0, nil) })
	inner := &recRunner{}
	return &wrapEnv{
		r: &wrapRunner{
			inner:        inner,
			evalSymlinks: func(p string) (string, error) { return p, nil },
			usable:       func(string) bool { return true },
			getenv: func(k string) string {
				return map[string]string{"XDG_STATE_HOME": "/state"}[k]
			},
		},
		inner: inner,
		ctx:   ctx,
		rec:   rec,
	}
}

func (e *wrapEnv) only(t *testing.T) Call {
	t.Helper()
	if len(e.inner.calls) != 1 {
		t.Fatalf("inner calls = %d, want 1", len(e.inner.calls))
	}
	return e.inner.calls[0]
}

// traceparentOf extracts and sanity-checks the --traceparent value in args.
func traceparentOf(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "--traceparent" && i+1 < len(args) {
			tp := args[i+1]
			parts := strings.Split(tp, "-")
			if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
				t.Fatalf("malformed traceparent %q", tp)
			}
			return tp
		}
	}
	t.Fatalf("no --traceparent in %v", args)
	return ""
}

func TestWrap_UserForm_ExactArgv(t *testing.T) {
	e := newWrapEnv(t, "/wrapper/pg-nix-log-wrapped")
	args := []string{"build", "--flake", "/x#host", "--override-input", "a", "path:/x"}
	if _, err := e.r.Run(e.ctx, "darwin-rebuild", args, RunOptions{WrapNix: true, Dir: "/d"}); err != nil {
		t.Fatal(err)
	}
	c := e.only(t)
	tp := traceparentOf(t, c.Args)
	want := append([]string{
		"--traceparent", tp,
		"--otlp-endpoint", testEndpoint,
		"--log-dir", "/state/pn/nix-logs",
		"--",
		"darwin-rebuild",
	}, args...)
	if c.Name != "/wrapper/pg-nix-log-wrapped" || !reflect.DeepEqual(c.Args, want) {
		t.Errorf("got %s %q\nwant /wrapper/pg-nix-log-wrapped %q", c.Name, c.Args, want)
	}
	if c.Opts.Dir != "/d" {
		t.Errorf("Opts.Dir not passed through: %+v", c.Opts)
	}
}

func TestWrap_SudoForm_ExactArgv(t *testing.T) {
	e := newWrapEnv(t, "/profiles/bin/pg-nix-log-wrapped")
	// The configured path is a symlink; sudo MUST execute the resolved real path.
	e.r.evalSymlinks = func(string) (string, error) { return storeWrapper, nil }
	args := []string{"darwin-rebuild", "switch", "--flake", "/x#host", "--override-input", "a", "path:/x"}
	if _, err := e.r.Run(e.ctx, "sudo", args, RunOptions{WrapNix: true}); err != nil {
		t.Fatal(err)
	}
	c := e.only(t)
	tp := traceparentOf(t, c.Args)
	// A root wrapper ignores --log-dir, so it is not passed under sudo.
	want := append([]string{
		storeWrapper,
		"--traceparent", tp,
		"--otlp-endpoint", testEndpoint,
		"--",
	}, args...)
	if c.Name != "sudo" || !reflect.DeepEqual(c.Args, want) {
		t.Errorf("got %s %q\nwant sudo %q", c.Name, c.Args, want)
	}
}

// The traceparent names the pn.exec span in ctx, so nix.invocation nests under it.
func TestWrap_TraceparentIsTheExecSpan(t *testing.T) {
	tel, rec := telemetrytest.New()
	res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, WrapperPath: "/w/pg-nix-log-wrapped"}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx = telemetrycfg.WithRunState(ctx, telemetrycfg.NewRunState(res))
	ctx, verb := tel.StartVerb(ctx, "workspace build")

	inner := &recRunner{}
	w := &wrapRunner{inner: inner, evalSymlinks: func(p string) (string, error) { return p, nil }, usable: func(string) bool { return true }, getenv: func(string) string { return "" }}
	if _, err := WithTracing(w).Run(ctx, "nix", []string{"build"}, RunOptions{WrapNix: true}); err != nil {
		t.Fatal(err)
	}
	verb.End(0, nil)

	tp := traceparentOf(t, inner.calls[0].Args)
	execs := rec.ByName("pn.exec")
	if len(execs) != 1 {
		t.Fatalf("pn.exec spans = %d", len(execs))
	}
	sc := execs[0].SpanContext()
	if want := fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags()); tp != want {
		t.Errorf("traceparent = %s, want the pn.exec span %s", tp, want)
	}
	// The exec span still names the command pn asked for, not the wrapper.
	var name string
	for _, kv := range execs[0].Attributes() {
		if string(kv.Key) == telemetry.AttrExecutable {
			name = kv.Value.AsString()
		}
	}
	if name != "nix" {
		t.Errorf("process.executable.name = %q, want nix", name)
	}
	// No log-dir without XDG_STATE_HOME/HOME: the wrapper picks its default.
	if containsArg(inner.calls[0].Args, "--log-dir") {
		t.Errorf("--log-dir passed although no state dir is known: %v", inner.calls[0].Args)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestWrap_SudoFormTable pins the exact sudo form: sudo followed DIRECTLY by
// darwin-rebuild, nixos-rebuild or nix. Everything else MUST run unwrapped.
func TestWrap_SudoFormTable(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		args     []string
		wantWrap bool
	}{
		// Positive: the forms pn emits.
		{"sudo darwin-rebuild", "sudo", []string{"darwin-rebuild", "switch", "--flake", "."}, true},
		{"sudo nixos-rebuild", "sudo", []string{"nixos-rebuild", "switch"}, true},
		{"sudo nix", "sudo", []string{"nix", "build", ".#x"}, true},
		{"bare darwin-rebuild", "darwin-rebuild", []string{"build"}, true},
		{"bare nixos-rebuild", "nixos-rebuild", []string{"build"}, true},
		{"bare nix", "nix", []string{"flake", "check"}, true},
		// NEGATIVE: sudo options or intermediate commands.
		{"sudo -E", "sudo", []string{"-E", "darwin-rebuild", "switch"}, false},
		{"sudo -u x", "sudo", []string{"-u", "x", "darwin-rebuild", "switch"}, false},
		{"sudo -n", "sudo", []string{"-n", "nix", "build"}, false},
		{"sudo env", "sudo", []string{"env", "FOO=1", "darwin-rebuild", "switch"}, false},
		{"sudo alone", "sudo", nil, false},
		{"sudo --", "sudo", []string{"--", "nix", "build"}, false},
		// NEGATIVE: other commands under sudo and the store verbs.
		{"sudo nix-store", "sudo", []string{"nix-store", "--gc"}, false},
		{"sudo nix-env", "sudo", []string{"nix-env", "--profile", "/p", "--list-generations"}, false},
		{"sudo sh -c", "sudo", []string{"sh", "-c", "darwin-rebuild switch"}, false},
		{"sudo abs path", "sudo", []string{"/run/current-system/sw/bin/darwin-rebuild", "switch"}, false},
		// NEGATIVE: build_command / apply_command with another argv[0].
		{"sh -c", "sh", []string{"-c", "nix build"}, false},
		{"script", "./build.sh", nil, false},
		{"nix-store", "nix-store", []string{"--gc"}, false},
		{"nix-env", "nix-env", []string{"-q"}, false},
		{"nixos-rebuild-ng", "nixos-rebuild-ng", []string{"switch"}, false},
		{"abs nix", "/nix/store/x-nix/bin/nix", []string{"build"}, false},
		{"git", "git", []string{"status"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newWrapEnv(t, storeWrapper)
			if _, err := e.r.Run(e.ctx, tc.cmd, tc.args, RunOptions{WrapNix: true}); err != nil {
				t.Fatal(err)
			}
			c := e.only(t)
			wrapped := containsArg(c.Args, "--otlp-endpoint")
			if wrapped != tc.wantWrap {
				t.Fatalf("%s %q: wrapped = %v, want %v (got %s %q)", tc.cmd, tc.args, wrapped, tc.wantWrap, c.Name, c.Args)
			}
			if !tc.wantWrap && (c.Name != tc.cmd || !reflect.DeepEqual(c.Args, tc.args)) {
				t.Errorf("unwrapped call altered: got %s %q, want %s %q", c.Name, c.Args, tc.cmd, tc.args)
			}
		})
	}
}

// Fail open: every condition that makes the wrapper unusable runs the original
// command with the original argv and options, byte for byte.
func TestWrap_FailsOpenToIdenticalCall(t *testing.T) {
	args := []string{"build", "--flake", "/x#h", "--override-input", "a", "path:/x"}
	opts := RunOptions{WrapNix: true, Dir: "/d", Env: map[string]string{"K": "v"}}

	tests := []struct {
		name  string
		cmd   string
		setup func(*wrapEnv) context.Context
	}{
		{"WrapNix false", "nix", func(e *wrapEnv) context.Context { return e.ctx }},
		{"no RunState", "nix", func(e *wrapEnv) context.Context {
			return telemetry.WithTelemetry(context.Background(), mustTel())
		}},
		{"telemetry disabled in resolution", "nix", func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: false, WrapperPath: "/w", Forced: "--no-telemetry"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}},
		{"no endpoint", "nix", func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, WrapperPath: "/w"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}},
		{"no wrapperPath configured", "nix", func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}},
		{"Telemetry not enabled", "nix", func(e *wrapEnv) context.Context {
			return telemetry.WithTelemetry(e.ctx, telemetry.Disabled())
		}},
		{"wrapper missing", "nix", func(e *wrapEnv) context.Context {
			e.r.usable = func(string) bool { return false }
			return e.ctx
		}},
		{"relative wrapper path", "nix", func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, WrapperPath: "rel/wrapper"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}},
		{"sudo: wrapper outside /nix/store", "sudo", func(e *wrapEnv) context.Context {
			e.r.evalSymlinks = func(string) (string, error) { return "/Users/me/bin/wrapper", nil }
			return e.ctx
		}},
		{"sudo: wrapper does not resolve", "sudo", func(e *wrapEnv) context.Context {
			e.r.evalSymlinks = func(string) (string, error) { return "", os.ErrNotExist }
			return e.ctx
		}},
		{"sudo: wrapper missing", "sudo", func(e *wrapEnv) context.Context {
			e.r.usable = func(string) bool { return false }
			return e.ctx
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newWrapEnv(t, storeWrapper)
			callArgs := args
			if tc.cmd == "sudo" {
				callArgs = append([]string{"darwin-rebuild"}, args...)
			}
			o := opts
			if tc.name == "WrapNix false" {
				o.WrapNix = false
			}
			ctx := tc.setup(e)
			if _, err := e.r.Run(ctx, tc.cmd, callArgs, o); err != nil {
				t.Fatal(err)
			}
			c := e.only(t)
			if c.Name != tc.cmd || !reflect.DeepEqual(c.Args, callArgs) || !reflect.DeepEqual(c.Opts, o) {
				t.Errorf("not identical: got %s %q %+v, want %s %q %+v", c.Name, c.Args, c.Opts, tc.cmd, callArgs, o)
			}
		})
	}
}

func mustTel() *telemetry.Telemetry {
	tel, _ := telemetrytest.New()
	return tel
}

// A failing wrapped command's error reads as the command pn asked for, not the
// wrapper argv, so messages and CommandError consumers are unchanged.
func TestWrap_CommandErrorNamesTheOriginalCommand(t *testing.T) {
	e := newWrapEnv(t, "/w/pg-nix-log-wrapped")
	e.inner.res = Result{ExitCode: 7, Stderr: []byte("boom")}
	e.inner.err = &CommandError{Name: "/w/pg-nix-log-wrapped", Args: []string{"--", "nix", "build"}, Result: e.inner.res}
	res, err := e.r.Run(e.ctx, "nix", []string{"build"}, RunOptions{WrapNix: true})
	var ce *CommandError
	if !errors.As(err, &ce) || res.ExitCode != 7 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if ce.Name != "nix" || !reflect.DeepEqual(ce.Args, []string{"build"}) {
		t.Errorf("CommandError = %s %v, want nix [build]", ce.Name, ce.Args)
	}
	if got, want := err.Error(), "nix exited 7: boom"; got != want {
		t.Errorf("error text = %q, want %q", got, want)
	}
}

// With telemetry off the whole decorated stack is behaviorally identical to the
// bare runner: same argv recorded by the command, same Result, same error text.
func TestWrap_TelemetryOffIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	argvOut := filepath.Join(dir, "argv")
	fakeCmd(t, dir, "nix")
	wrapper := fakeWrapper(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := trickyArgs()
	opts := func() RunOptions {
		return RunOptions{WrapNix: true, Env: map[string]string{"ARGV_OUT": argvOut}}
	}

	run := func(r Runner, ctx context.Context) (Result, []byte) {
		_ = os.Remove(argvOut)
		res, err := r.Run(ctx, "nix", args, opts())
		if err != nil {
			t.Fatal(err)
		}
		got, rerr := os.ReadFile(argvOut)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return res, got
	}

	baseRes, baseArgv := run(&realRunner{}, context.Background())

	// Stack with telemetry off: no RunState, then an explicitly forced-off one.
	offRes := telemetrycfg.Resolution{Forced: "--no-telemetry", WrapperPath: wrapper}
	offCtx := telemetrycfg.WithRunState(context.Background(), telemetrycfg.NewRunState(offRes))
	for name, ctx := range map[string]context.Context{"no run state": context.Background(), "forced off": offCtx} {
		res, argv := run(NewRealRunner(), ctx)
		if !bytes.Equal(argv, baseArgv) || !bytes.Equal(res.Stdout, baseRes.Stdout) || res.ExitCode != baseRes.ExitCode {
			t.Errorf("%s: differs from the bare runner:\nargv %q vs %q\nresult %+v vs %+v", name, argv, baseArgv, res, baseRes)
		}
	}
}

// trickyArgs are arguments that look like wrapper flags, a stray "--", and
// --override-input pairs: the wrapper MUST forward every one verbatim.
func trickyArgs() []string {
	return []string{
		"build", "--flake", "/w/consumer#host",
		"--override-input", "producer", "path:/w/producer",
		"--traceparent", "00-deadbeef-cafe-01",
		"--log-dir", "/elsewhere",
		"--otlp-endpoint", "http://evil:1",
		"--", "--check", "",
		"--override-input", "a b", "git+file:///x y",
	}
}

// fakeCmd installs a script named name in dir that records its argv, NUL
// separated, into $ARGV_OUT and prints a marker.
func fakeCmd(t *testing.T, dir, name string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$ARGV_OUT\"\necho ran-" + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeWrapper installs a stand-in for pg-nix-log-wrapped: it drops its own
// flags up to the first "--" and execs the rest, like the real wrapper's
// argv contract (ADR 0030), and records its own argv to $WRAPPER_OUT if set.
func fakeWrapper(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "pg-nix-log-wrapped")
	script := `#!/bin/sh
if [ -n "$WRAPPER_OUT" ]; then printf '%s\0' "$@" > "$WRAPPER_OUT"; fi
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWrap_ArgvByteForByteWithRealProcesses is the --override-input safety
// proof: the command at the end of the chain records the exact argv it receives
// with and without the wrapper, and the two MUST be byte-for-byte equal even
// when the arguments look like wrapper flags.
func TestWrap_ArgvByteForByteWithRealProcesses(t *testing.T) {
	dir := t.TempDir()
	fakeCmd(t, dir, "nix")
	wrapper := fakeWrapper(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	argvOut := filepath.Join(dir, "argv")
	wrapperOut := filepath.Join(dir, "wrapper-argv")
	args := trickyArgs()

	// Baseline: no telemetry, no wrapper.
	var plainOut bytes.Buffer
	if _, err := NewRealRunner().Run(context.Background(), "nix", args,
		RunOptions{WrapNix: true, Stdout: &plainOut, Env: map[string]string{"ARGV_OUT": argvOut}}); err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(argvOut)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(argvOut)

	// Wrapped: telemetry on, wrapper configured.
	tel, _ := telemetrytest.New()
	res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, WrapperPath: wrapper}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx = telemetrycfg.WithRunState(ctx, telemetrycfg.NewRunState(res))
	var wrappedOut bytes.Buffer
	if _, err := NewRealRunner().Run(ctx, "nix", args, RunOptions{
		WrapNix: true, Stdout: &wrappedOut,
		Env: map[string]string{"ARGV_OUT": argvOut, "WRAPPER_OUT": wrapperOut},
	}); err != nil {
		t.Fatal(err)
	}
	wrapped, err := os.ReadFile(argvOut)
	if err != nil {
		t.Fatal(err)
	}

	// The wrapper really ran (its own argv was recorded) ...
	wargv, err := os.ReadFile(wrapperOut)
	if err != nil {
		t.Fatalf("wrapper did not run: %v", err)
	}
	if !bytes.HasPrefix(wargv, []byte("--traceparent\x00")) || !bytes.Contains(wargv, []byte("\x00--\x00nix\x00build\x00")) {
		t.Errorf("wrapper argv = %q", wargv)
	}
	// ... and the command still received byte-identical argv and printed the same.
	if !bytes.Equal(plain, wrapped) {
		t.Errorf("argv differs with the wrapper:\nplain   %q\nwrapped %q", plain, wrapped)
	}
	if want := strings.Join(args, "\x00") + "\x00"; string(plain) != want {
		t.Errorf("recorded argv %q does not match input %q", plain, want)
	}
	if plainOut.String() != wrappedOut.String() {
		t.Errorf("stdout differs: %q vs %q", plainOut.String(), wrappedOut.String())
	}
}
