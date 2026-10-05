package exec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// onCtx returns a context carrying enabled telemetry and a valid pn.exec-like
// span, plus that span's context.
func onCtx(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	tel, _ := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, end := telemetry.StartExec(ctx, "test")
	t.Cleanup(func() { end(0, nil) })
	return ctx, trace.SpanContextFromContext(ctx)
}

func TestTraceEnvRunner_TelemetryOn_ExportsTraceparentFromSpan(t *testing.T) {
	ctx, sc := onCtx(t)
	inner := &recRunner{}
	callerEnv := map[string]string{"UL_LIB_DIR": "/x"}
	_, err := WithTraceEnv(inner).Run(ctx, "./update-locks.sh", []string{"a", "--", "b c"}, RunOptions{Dir: "/d", Env: callerEnv, PropagateTrace: true})
	if err != nil {
		t.Fatal(err)
	}
	c := inner.calls[0]
	tp := c.Opts.Env["TRACEPARENT"]
	if !traceparentRe.MatchString(tp) {
		t.Fatalf("TRACEPARENT = %q, want W3C traceparent", tp)
	}
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String(); tp != want {
		t.Errorf("TRACEPARENT = %q, want the current span %q", tp, want)
	}
	if c.Opts.Env["UL_LIB_DIR"] != "/x" || c.Opts.Dir != "/d" {
		t.Errorf("other options lost: %+v", c.Opts)
	}
	// argv is never rewritten.
	if c.Name != "./update-locks.sh" || !reflect.DeepEqual(c.Args, []string{"a", "--", "b c"}) {
		t.Errorf("argv rewritten: %s %q", c.Name, c.Args)
	}
	if _, mutated := callerEnv["TRACEPARENT"]; mutated || len(callerEnv) != 1 {
		t.Errorf("caller's Env map was mutated: %v", callerEnv)
	}
}

func TestTraceEnvRunner_TelemetryOn_NilEnvGetsOnlyTraceparent(t *testing.T) {
	ctx, _ := onCtx(t)
	inner := &recRunner{}
	if _, err := WithTraceEnv(inner).Run(ctx, "sh", []string{"-c", "true"}, RunOptions{PropagateTrace: true}); err != nil {
		t.Fatal(err)
	}
	env := inner.calls[0].Opts.Env
	if len(env) != 1 || !traceparentRe.MatchString(env["TRACEPARENT"]) {
		t.Errorf("env = %v, want exactly TRACEPARENT", env)
	}
}

// Telemetry off (no Telemetry, or the disabled one) leaves opts byte-identical.
func TestTraceEnvRunner_TelemetryOff_OptionsUntouched(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no telemetry in ctx": context.Background(),
		"disabled telemetry":  telemetry.WithTelemetry(context.Background(), telemetry.Disabled()),
	} {
		t.Run(name, func(t *testing.T) {
			for _, env := range []map[string]string{nil, {"A": "1"}} {
				inner := &recRunner{}
				opts := RunOptions{Dir: "/d", Env: env, PropagateTrace: true}
				if _, err := WithTraceEnv(inner).Run(ctx, "sh", []string{"-c", "true"}, opts); err != nil {
					t.Fatal(err)
				}
				if got := inner.calls[0]; !reflect.DeepEqual(got.Opts, opts) {
					t.Errorf("opts changed with telemetry off: got %+v want %+v", got.Opts, opts)
				}
			}
		})
	}
}

// A call that does not opt in is never touched, even with telemetry on.
func TestTraceEnvRunner_NotOptedIn_OptionsUntouched(t *testing.T) {
	ctx, _ := onCtx(t)
	inner := &recRunner{}
	opts := RunOptions{Env: map[string]string{"A": "1"}}
	if _, err := WithTraceEnv(inner).Run(ctx, "git", []string{"status"}, opts); err != nil {
		t.Fatal(err)
	}
	if got := inner.calls[0].Opts; !reflect.DeepEqual(got, opts) {
		t.Errorf("opts changed without PropagateTrace: %+v", got)
	}
}

// Enabled telemetry but no valid span in ctx: nothing to propagate.
func TestTraceEnvRunner_NoValidSpan_OptionsUntouched(t *testing.T) {
	tel, _ := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	inner := &recRunner{}
	opts := RunOptions{PropagateTrace: true}
	if _, err := WithTraceEnv(inner).Run(ctx, "sh", nil, opts); err != nil {
		t.Fatal(err)
	}
	if got := inner.calls[0].Opts; !reflect.DeepEqual(got, opts) {
		t.Errorf("opts changed with no span: %+v", got)
	}
}

// End-to-end through the production stack: the child sees the pn.exec span's
// TRACEPARENT with telemetry on, and exactly the inherited one (none) with
// telemetry off; stdout is otherwise identical.
func TestNewRealRunner_PropagateTrace_ChildEnvironment(t *testing.T) {
	t.Setenv("TRACEPARENT", "")
	probe := []string{"-c", `printf '%s' "${TRACEPARENT-unset}"`}

	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx, verb := tel.StartVerb(ctx, "workspace update")
	var out bytes.Buffer
	if _, err := NewRealRunner().Run(ctx, "sh", probe, RunOptions{PropagateTrace: true, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	verb.End(0, nil)
	es := rec.ByName("pn.exec")
	if len(es) != 1 {
		t.Fatalf("pn.exec spans = %d, want 1", len(es))
	}
	want := "00-" + es[0].SpanContext().TraceID().String() + "-" + es[0].SpanContext().SpanID().String() + "-" + es[0].SpanContext().TraceFlags().String()
	if out.String() != want {
		t.Errorf("child TRACEPARENT = %q, want pn.exec span %q", out.String(), want)
	}
	if !strings.HasPrefix(out.String(), "00-"+rec.ByName("pn.verb")[0].SpanContext().TraceID().String()+"-") {
		t.Error("child TRACEPARENT is not in the pn.verb trace")
	}

	// Telemetry off: the child inherits the parent's environment unchanged.
	t.Setenv("TRACEPARENT", "inherited")
	out.Reset()
	if _, err := NewRealRunner().Run(context.Background(), "sh", probe, RunOptions{PropagateTrace: true, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "inherited" {
		t.Errorf("telemetry off: child TRACEPARENT = %q, want the inherited value", out.String())
	}
}

// wrapperCtx returns a telemetry-on context whose RunState names wrapper.
func wrapperCtx(t *testing.T, wrapper string) context.Context {
	t.Helper()
	ctx, _ := onCtx(t)
	return telemetrycfg.WithRunState(ctx, telemetrycfg.NewRunState(telemetrycfg.Resolution{
		Enabled: true, Endpoint: testEndpoint, Source: telemetrycfg.SourceFlag, WrapperPath: wrapper,
	}))
}

// With a usable wrapper the child also learns where it is and which endpoint
// to hand it, so update-locks.sh can run its nix calls under it.
func TestTraceEnvRunner_UsableWrapper_ExportsWrapperAndEndpoint(t *testing.T) {
	ctx := wrapperCtx(t, storeWrapper)
	inner := &recRunner{}
	r := &traceEnvRunner{inner: inner, usable: func(string) bool { return true }}
	if _, err := r.Run(ctx, "./update-locks.sh", nil, RunOptions{PropagateTrace: true, Env: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	env := inner.calls[0].Opts.Env
	if env[EnvNixWrapper] != storeWrapper || env[EnvNixWrapperEndpoint] != testEndpoint || env["A"] != "1" {
		t.Errorf("env = %v", env)
	}
	if !traceparentRe.MatchString(env["TRACEPARENT"]) {
		t.Errorf("TRACEPARENT missing: %v", env)
	}
}

// An unusable wrapper (absent, not executable, relative, not configured) is
// not advertised: the env carries TRACEPARENT only, as before.
func TestTraceEnvRunner_UnusableWrapper_ExportsOnlyTraceparent(t *testing.T) {
	for name, tc := range map[string]struct {
		wrapper string
		usable  bool
	}{
		"not executable": {storeWrapper, false},
		"relative":       {"pg-nix-log-wrapped", true},
		"unconfigured":   {"", true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := wrapperCtx(t, tc.wrapper)
			inner := &recRunner{}
			r := &traceEnvRunner{inner: inner, usable: func(string) bool { return tc.usable }}
			if _, err := r.Run(ctx, "sh", nil, RunOptions{PropagateTrace: true}); err != nil {
				t.Fatal(err)
			}
			env := inner.calls[0].Opts.Env
			if len(env) != 1 || !traceparentRe.MatchString(env["TRACEPARENT"]) {
				t.Errorf("env = %v, want exactly TRACEPARENT", env)
			}
		})
	}
}

// Telemetry off: the wrapper is never advertised, opts are untouched.
func TestTraceEnvRunner_TelemetryOff_WrapperNotAdvertised(t *testing.T) {
	ctx := telemetrycfg.WithRunState(context.Background(), telemetrycfg.NewRunState(telemetrycfg.Resolution{
		Enabled: true, Endpoint: testEndpoint, WrapperPath: storeWrapper,
	}))
	inner := &recRunner{}
	r := &traceEnvRunner{inner: inner, usable: func(string) bool { return true }}
	opts := RunOptions{PropagateTrace: true}
	if _, err := r.Run(ctx, "sh", nil, opts); err != nil {
		t.Fatal(err)
	}
	if got := inner.calls[0].Opts; !reflect.DeepEqual(got, opts) {
		t.Errorf("opts changed with telemetry off: %+v", got)
	}
}

func TestNixWrapperArgv(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	// storeWrapper does not exist on disk, so the production probe rejects it.
	if got := NixWrapperArgv(wrapperCtx(t, storeWrapper)); got != nil {
		t.Errorf("nonexistent wrapper: got %v, want nil", got)
	}
	real := filepath.Join(t.TempDir(), "w")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []string{real, "--otlp-endpoint", testEndpoint, "--log-dir", "/state/pn/nix-logs", "--"}
	if got := NixWrapperArgv(wrapperCtx(t, real)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := NixWrapperArgv(context.Background()); got != nil {
		t.Errorf("no telemetry: got %v, want nil", got)
	}
}
