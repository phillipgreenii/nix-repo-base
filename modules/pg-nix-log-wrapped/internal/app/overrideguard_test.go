package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
)

// The two warnings nix 2.34 writes into its json log for an unmatched
// --override-input (colour codes included, as nix emits them).
var unmatchedWarnings = []string{
	`{"action":"msg","level":1,"msg":"\u001b[35;1mwarning:\u001b[0m input '' has an override for a non-existent input 'bogus'"}`,
	`{"action":"msg","level":1,"msg":"\u001b[35;1mwarning:\u001b[0m the flag '--override-input bogus path:/x' does not match any input"}`,
}

func TestHasOverrideInput(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"nix", "build", "--override-input", "a", "b"}, true},
		{[]string{"nix", "build", "."}, false},
		{[]string{"nix", "run", ".", "--", "--override-input", "a", "b"}, false},
		{[]string{"nix", "--override-input", "a", "b", "run", ".", "--", "x"}, true},
		{[]string{"nix", "--override-inputs"}, false},
	}
	for _, c := range cases {
		if got := hasOverrideInput(c.args); got != c.want {
			t.Errorf("hasOverrideInput(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestUnmatchedOverrideTurnsExitZeroIntoNonZeroAndNamesIt(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Lines: unmatchedWarnings, Stdout: "2\n"})
	code := r.run("fake-nix", "eval", ".#v", "--override-input", "bogus", "path:/x")
	if code != ExitUnmatchedOverride {
		t.Fatalf("exit = %d, want %d; stderr %q", code, ExitUnmatchedOverride, r.stderr)
	}
	for _, want := range []string{"non-existent input 'bogus'", "does not match any input", "phillipgreenii-*", AllowUnmatchedOverrideEnv} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, r.stderr)
		}
	}
	if strings.Contains(r.stderr.String(), "\x1b[") {
		t.Errorf("ANSI escapes leaked into the message: %q", r.stderr)
	}
	if r.childOut.String() != "2\n" {
		t.Errorf("child stdout altered: %q", r.childOut)
	}
}

func TestUnmatchedOverrideGuardLeavesOtherRunsAlone(t *testing.T) {
	cases := []struct {
		name string
		spec fakecmd.Spec
		env  []string
		args []string
		want int
	}{
		{
			"no --override-input flag",
			fakecmd.Spec{Lines: unmatchedWarnings},
			nil,
			[]string{"fake-nix", "eval", "."},
			0,
		},
		{
			"flag only after --",
			fakecmd.Spec{Lines: unmatchedWarnings},
			nil,
			[]string{"fake-nix", "run", ".", "--", "--override-input", "a", "b"},
			0,
		},
		{
			"valid override (no warning)",
			fakecmd.Spec{},
			nil,
			[]string{"fake-nix", "eval", ".", "--override-input", "a", "b"},
			0,
		},
		{
			"already failing run keeps its own code",
			fakecmd.Spec{Lines: unmatchedWarnings, Exit: 7},
			nil,
			[]string{"fake-nix", "eval", ".", "--override-input", "bogus", "b"},
			7,
		},
		{
			"escape hatch",
			fakecmd.Spec{Lines: unmatchedWarnings},
			[]string{AllowUnmatchedOverrideEnv + "=1"},
			[]string{"fake-nix", "eval", ".", "--override-input", "bogus", "b"},
			0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.spec, c.env...)
			if code := r.run(c.args...); code != c.want {
				t.Errorf("exit = %d, want %d", code, c.want)
			}
			if r.stderr.Len() != 0 {
				t.Errorf("wrapper stderr must stay empty, got %q", r.stderr)
			}
		})
	}
}

func TestUnmatchedOverrideGuardWorksWithTelemetryOff(t *testing.T) {
	for _, off := range []string{"no endpoint", "invalid endpoint"} {
		t.Run(off, func(t *testing.T) {
			r := newRig(t, fakecmd.Spec{Lines: unmatchedWarnings})
			if off == "no endpoint" {
				r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
			} else {
				r.mem.fail = errors.New("boom")
			}
			code := r.run("fake-nix", "eval", ".", "--override-input", "bogus", "x")
			if code != ExitUnmatchedOverride {
				t.Fatalf("exit = %d, want %d; stderr %q", code, ExitUnmatchedOverride, r.stderr)
			}
			if len(r.execs) != 0 {
				t.Errorf("guard-only run must not exec-replace the wrapper, execs=%d", len(r.execs))
			}
			if !strings.Contains(r.stderr.String(), "'bogus'") {
				t.Errorf("stderr = %q", r.stderr)
			}
		})
	}
}

func TestTelemetryOffPassthroughIsUnchangedWithoutOverrideInput(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Lines: unmatchedWarnings})
	r.d.Environ = filterEnv(r.d.Environ, "OTEL_EXPORTER_OTLP_ENDPOINT")
	if code := r.run("fake-nix", "eval", "."); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(r.execs) != 1 {
		t.Fatalf("execs = %d, want the plain fail-open exec", len(r.execs))
	}
}

func TestExplicitDisableStaysPurePassthroughEvenWithOverrideInput(t *testing.T) {
	r := newRig(t, fakecmd.Spec{Lines: unmatchedWarnings}, "PG_NIX_LOG_DISABLE=1")
	if code := r.run("fake-nix", "eval", ".", "--override-input", "bogus", "x"); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(r.execs) != 1 {
		t.Fatalf("execs = %d, want 1", len(r.execs))
	}
}
