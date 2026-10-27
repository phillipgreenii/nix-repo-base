package exec

import (
	"context"
	"errors"
	"os"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

func spanAttrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value
	}
	return m
}

// execSpan runs one call through the tracing decorator over wrap (the
// production order: tracing outside, wrapping inside) and returns the pn.exec
// span it produced.
func execSpan(t *testing.T, ctx context.Context, rec *telemetrytest.Recorder, wrap *wrapRunner, name string, args []string, opts RunOptions) (sdktrace.ReadOnlySpan, error) {
	t.Helper()
	before := len(rec.ByName(telemetry.SpanExec))
	_, err := WithTracing(wrap).Run(ctx, name, args, opts)
	all := rec.ByName(telemetry.SpanExec)
	if len(all) != before+1 {
		t.Fatalf("pn.exec spans = %d, want %d", len(all), before+1)
	}
	return all[len(all)-1], err
}

func TestExecSubcommand(t *testing.T) {
	rootEnv := []string{"env", "-i", "HOME=/var/root", "NIX_REMOTE=daemon", "PATH=/run/current-system/sw/bin"}
	tests := []struct {
		name string
		cmd  string
		args []string
		want string
	}{
		{"darwin-rebuild build", "darwin-rebuild", []string{"build", "--flake", "/x#h", "--override-input", "a", "path:/x"}, "darwin-rebuild build"},
		{"sudo darwin-rebuild switch", "sudo", []string{"darwin-rebuild", "switch", "--flake", "/x#h"}, "darwin-rebuild switch"},
		{"flag value before the verb", "darwin-rebuild", []string{"--flake", "/x#h", "switch"}, "darwin-rebuild switch"},
		{"root activate by store path", "sudo", []string{"/nix/store/aaa-darwin-rebuild/sw/bin/darwin-rebuild", "activate"}, "darwin-rebuild activate"},
		{"root nix-env via sudo env -i", "sudo", append(append([]string{}, rootEnv...), "/run/current-system/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", "/nix/store/x"), "nix-env"},
		{"sudo env without -i", "sudo", []string{"env", "A=1", "B_2=x=y", "/nix/store/x/bin/darwin-rebuild", "activate"}, "darwin-rebuild activate"},
		{"sudo -E", "sudo", []string{"-E", "darwin-rebuild", "check"}, "darwin-rebuild check"},
		{"absolute argv0, no sudo", "/run/current-system/sw/bin/nix", []string{"flake", "check"}, "nix flake check"},
		{"nix flake check", "nix", []string{"flake", "check", "--no-build", "/repo"}, "nix flake check"},
		{"nix value flag between words", "nix", []string{"--option", "build-cores", "4", "flake", "update"}, "nix flake update"},
		{"nix --override-input value", "nix", []string{"flake", "check", "--override-input", "a", "git+file:///x"}, "nix flake check"},
		{"nix build", "nix", []string{"build", ".#x"}, "nix build"},
		{"nix not allowlisted", "nix", []string{"hash", "path", "/x"}, SubcommandOther},
		{"darwin-rebuild not allowlisted", "darwin-rebuild", []string{"rollback"}, SubcommandOther},
		{"darwin-rebuild no verb", "darwin-rebuild", []string{"--flake", "/x#h"}, SubcommandOther},
		{"unrelated tool", "git", []string{"status", "--porcelain"}, SubcommandOther},
		{"sh", "sh", []string{"-c", "darwin-rebuild switch"}, SubcommandOther},
		{"sudo alone", "sudo", nil, SubcommandOther},
		{"sudo env alone", "sudo", []string{"env", "-i", "A=1"}, SubcommandOther},
		{"env assignment lookalike is not skipped", "sudo", []string{"env", "1A=x", "nix-env"}, SubcommandOther},
		{"nixos-rebuild switch", "nixos-rebuild", []string{"switch", "--flake", "/x#h"}, "nixos-rebuild switch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecSubcommand(tc.cmd, tc.args); got != tc.want {
				t.Errorf("ExecSubcommand(%q, %q) = %q, want %q", tc.cmd, tc.args, got, tc.want)
			}
		})
	}
}

// Every pn.exec span carries the allowlisted subcommand; the executable name
// stays what it was (sudo for the sudo forms).
func TestTracedRunner_RecordsSubcommand(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	inner := &recRunner{}
	if _, err := WithTracing(inner).Run(ctx, "sudo", []string{"darwin-rebuild", "switch", "--flake", "/x#h"}, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := WithTracing(inner).Run(ctx, "git", []string{"status"}, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	spans := rec.ByName(telemetry.SpanExec)
	if len(spans) != 2 {
		t.Fatalf("pn.exec spans = %d, want 2", len(spans))
	}
	for i, want := range []struct{ exe, sub string }{{"sudo", "darwin-rebuild switch"}, {"git", SubcommandOther}} {
		a := spanAttrs(spans[i])
		if a["process.executable.name"].AsString() != want.exe || a["pn.exec.subcommand"].AsString() != want.sub {
			t.Errorf("span %d: name=%q subcommand=%q, want %q %q", i, a["process.executable.name"].AsString(), a["pn.exec.subcommand"].AsString(), want.exe, want.sub)
		}
	}
}

// A WrapNix call records wrapped=true when it ran under the wrapper, and
// wrapped=false with the reason for each way it can run unwrapped. A call that
// never asked to be wrapped carries neither attribute.
func TestWrap_SpanRecordsWrappedAndSkipReason(t *testing.T) {
	nixArgs := []string{"build", "--flake", "/x#h"}
	tests := []struct {
		name    string
		cmd     string
		args    []string
		opts    RunOptions
		setup   func(*wrapEnv) context.Context
		wrapped bool
		reason  string
	}{
		{"wrapped user form", "darwin-rebuild", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context { return e.ctx }, true, ""},
		{"wrapped sudo form", "sudo", append([]string{"darwin-rebuild"}, nixArgs...), RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			e.r.evalSymlinks = func(string) (string, error) { return storeWrapper, nil }
			return e.ctx
		}, true, ""},
		{"telemetry off in resolution", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: false, WrapperPath: "/w", Forced: "--no-telemetry"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}, false, "telemetry_off"},
		{"no run state", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			return telemetrycfg.WithRunState(e.ctx, nil)
		}, false, "telemetry_off"},
		{"no endpoint", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, WrapperPath: "/w"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}, false, "no_endpoint"},
		{"no wrapper path", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}, false, "no_wrapper_path"},
		{"unrecognized form: sudo -E", "sudo", append([]string{"-E", "darwin-rebuild"}, nixArgs...), RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context { return e.ctx }, false, "unrecognized_form"},
		{"unrecognized form: sh -c", "sh", []string{"-c", "darwin-rebuild build"}, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context { return e.ctx }, false, "unrecognized_form"},
		{"sudo wrapper outside the store", "sudo", append([]string{"darwin-rebuild"}, nixArgs...), RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			e.r.evalSymlinks = func(string) (string, error) { return "/Users/me/bin/wrapper", nil }
			return e.ctx
		}, false, "sudo_wrapper_not_in_store"},
		{"sudo wrapper does not resolve", "sudo", append([]string{"darwin-rebuild"}, nixArgs...), RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			e.r.evalSymlinks = func(string) (string, error) { return "", os.ErrNotExist }
			return e.ctx
		}, false, "sudo_wrapper_not_in_store"},
		{"relative wrapper path", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, WrapperPath: "rel/wrapper"}
			return telemetrycfg.WithRunState(e.ctx, telemetrycfg.NewRunState(res))
		}, false, "wrapper_not_absolute"},
		{"wrapper not executable", "nix", nixArgs, RunOptions{WrapNix: true}, func(e *wrapEnv) context.Context {
			e.r.usable = func(string) bool { return false }
			return e.ctx
		}, false, "wrapper_not_executable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newWrapEnv(t, storeWrapper)
			ctx := tc.setup(e)
			span, err := execSpan(t, ctx, e.rec, e.r, tc.cmd, tc.args, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			a := spanAttrs(span)
			w, ok := a["pn.exec.wrapped"]
			if !ok || w.AsBool() != tc.wrapped {
				t.Fatalf("pn.exec.wrapped = %v (present %v), want %v", w, ok, tc.wrapped)
			}
			r, hasReason := a["pn.exec.wrap_skip_reason"]
			if tc.wrapped && hasReason {
				t.Errorf("a wrapped call must not carry a skip reason, got %q", r.AsString())
			}
			if !tc.wrapped && r.AsString() != tc.reason {
				t.Errorf("pn.exec.wrap_skip_reason = %q, want %q", r.AsString(), tc.reason)
			}
		})
	}
}

func TestWrap_SpanHasNoWrapAttributesWhenNotRequested(t *testing.T) {
	e := newWrapEnv(t, storeWrapper)
	span, err := execSpan(t, e.ctx, e.rec, e.r, "git", []string{"status"}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := spanAttrs(span)
	if _, ok := a["pn.exec.wrapped"]; ok {
		t.Error("pn.exec.wrapped set on a call that did not ask to be wrapped")
	}
	if _, ok := a["pn.exec.wrap_skip_reason"]; ok {
		t.Error("pn.exec.wrap_skip_reason set on a call that did not ask to be wrapped")
	}
}

// Exits 130 and 143, and a -1 (signal death) exit, mark the pn.exec span
// error.type=signal with process.signal; an ordinary failure keeps its Go error
// type and has no process.signal.
func TestTracedRunner_SignalExits(t *testing.T) {
	tests := []struct {
		name     string
		res      Result
		wantExit int64
		wantSig  string
	}{
		{"SIGINT as 130", Result{ExitCode: 130}, 130, "SIGINT"},
		{"SIGTERM as 143", Result{ExitCode: 143}, 143, "SIGTERM"},
		{"signal death named by the runner", Result{ExitCode: -1, Signal: "SIGKILL"}, -1, "SIGKILL"},
		{"signal death with no name", Result{ExitCode: -1}, -1, "unknown"},
		{"ordinary failure", Result{ExitCode: 2}, 2, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tel, rec := telemetrytest.New()
			ctx := telemetry.WithTelemetry(context.Background(), tel)
			inner := &recRunner{res: tc.res}
			inner.err = &CommandError{Name: "nix", Result: tc.res}
			_, err := WithTracing(inner).Run(ctx, "nix", []string{"build"}, RunOptions{})
			var ce *CommandError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want the CommandError", err)
			}
			s := rec.ByName(telemetry.SpanExec)[0]
			a := spanAttrs(s)
			if s.Status().Code != codes.Error || a["process.exit.code"].AsInt64() != tc.wantExit {
				t.Errorf("status=%v exit=%v, want Error and %d", s.Status().Code, a["process.exit.code"], tc.wantExit)
			}
			sig, hasSig := a["process.signal"]
			if tc.wantSig == "" {
				if hasSig || a["error.type"].AsString() == "signal" {
					t.Errorf("an ordinary failure must not be marked a signal: %v", a)
				}
				return
			}
			if a["error.type"].AsString() != "signal" || sig.AsString() != tc.wantSig {
				t.Errorf("error.type=%q process.signal=%q, want signal %q", a["error.type"].AsString(), sig.AsString(), tc.wantSig)
			}
		})
	}
}

// The pn.verb span reports a signal exit the same way.
func TestVerbSpan_SignalExit(t *testing.T) {
	tel, rec := telemetrytest.New()
	_, verb := tel.StartVerb(context.Background(), "workspace apply")
	verb.End(143, errors.New("terminated"))
	a := spanAttrs(rec.ByName(telemetry.SpanVerb)[0])
	if a["error.type"].AsString() != "signal" || a["process.signal"].AsString() != "SIGTERM" {
		t.Errorf("verb attrs = %v, want error.type=signal process.signal=SIGTERM", a)
	}
}

// With a real child that kills itself (os/exec reports exit -1), the production
// runner names the signal and the pn.exec span carries it.
func TestRealRunner_SignalDeathIsNamedOnSpan(t *testing.T) {
	tel, rec := telemetrytest.New()
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	res, err := NewRealRunner().Run(ctx, "sh", []string{"-c", "kill -TERM $$"}, RunOptions{})
	var ce *CommandError
	if !errors.As(err, &ce) || res.ExitCode != -1 || res.Signal != "SIGTERM" || ce.ExitSignal() != "SIGTERM" {
		t.Fatalf("err=%v exit=%d signal=%q, want CommandError exit -1 SIGTERM", err, res.ExitCode, res.Signal)
	}
	a := spanAttrs(rec.ByName(telemetry.SpanExec)[0])
	if a["error.type"].AsString() != "signal" || a["process.signal"].AsString() != "SIGTERM" {
		t.Errorf("span attrs = %v, want error.type=signal process.signal=SIGTERM", a)
	}
}
