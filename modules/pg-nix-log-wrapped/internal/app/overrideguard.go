package app

// Unmatched --override-input guard (bead pg2-jvzmgf).
//
// nix only WARNS when `--override-input NAME URL` names an input the flake
// does not have, then builds against the LOCKED input: the run exits 0 and is
// silently wrong. The wrapper already receives nix's own json log, in which
// that warning is an {"action":"msg"} line, so the guard is an Observer on the
// same stream (no stderr capture: a pipe would change nix's tty behaviour).
//
// Exit-code contract: the guard only ever turns a would-be exit 0 into
// ExitUnmatchedOverride, and only for a CMD that carries --override-input.
// Runs without that flag, and runs that already fail, are untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/retain"
	"github.com/phillipgreenii/x/otelsetup"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// ExitUnmatchedOverride is the exit status of a run that nix finished with
	// status 0 although an --override-input matched no flake input. It is a
	// distinct value >= 2 so a caller can branch on it (1 stays generic).
	ExitUnmatchedOverride = 3
	// AllowUnmatchedOverrideEnv set to "1" turns the guard off.
	AllowUnmatchedOverrideEnv = "PG_NIX_LOG_ALLOW_UNMATCHED_OVERRIDE"
)

// unmatchedMarkers are the substrings of the two nix warnings:
//
//	the flag '--override-input NAME URL' does not match any input
//	input 'X' has an override for a non-existent input 'NAME'
var unmatchedMarkers = [][]byte{
	[]byte("does not match any input"),
	[]byte("has an override for a non-existent input"),
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// hasOverrideInput reports whether args carries nix's --override-input flag
// before any "--" (what follows "--" belongs to the program being run).
func hasOverrideInput(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--override-input" {
			return true
		}
	}
	return false
}

// overrideGuard is a retain.Observer that forwards every line and remembers
// the unmatched-override warnings. It is used by the tailer goroutine only;
// unmatched is read after that goroutine has finished.
type overrideGuard struct {
	next  retain.Observer
	found []string
}

// newOverrideGuard returns nil (guard off) unless CMD carries --override-input
// and the escape hatch is unset.
func newOverrideGuard(cmd []string, getenv func(string) string) *overrideGuard {
	if !hasOverrideInput(cmd) || getenv(AllowUnmatchedOverrideEnv) == "1" {
		return nil
	}
	return &overrideGuard{}
}

func (g *overrideGuard) Line(line []byte, at time.Time) {
	if g.next != nil {
		defer g.next.Line(line, at)
	}
	hit := false
	for _, m := range unmatchedMarkers {
		if bytes.Contains(line, m) {
			hit = true
			break
		}
	}
	if !hit {
		return
	}
	var ev struct {
		Action string `json:"action"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Action != "msg" {
		return
	}
	msg := strings.TrimSpace(ansiEscape.ReplaceAllString(ev.Msg, ""))
	for _, f := range g.found {
		if f == msg {
			return
		}
	}
	g.found = append(g.found, msg)
}

// unmatched returns the distinct warnings seen; nil-safe.
func (g *overrideGuard) unmatched() []string {
	if g == nil {
		return nil
	}
	return g.found
}

// report writes the failure explanation naming each unmatched override.
func (g *overrideGuard) report(w io.Writer) {
	fmt.Fprintf(w, "pg-nix-log-wrapped: nix only warned that an --override-input matches no flake input, "+
		"so it built against the LOCKED input; failing the run (exit %d).\n", ExitUnmatchedOverride)
	for _, m := range g.found {
		fmt.Fprintf(w, "pg-nix-log-wrapped:   %s\n", m)
	}
	fmt.Fprintf(w, "pg-nix-log-wrapped: override names are the flake INPUT names (phillipgreenii-*), "+
		"not repo directory names. Set %s=1 to allow.\n", AllowUnmatchedOverrideEnv)
}

// noopTelemetry is the Telemetry of a guard-only session: the run is observed
// for the guard but nothing is exported.
func noopTelemetry(context.Context, otelsetup.Config, ...otelsetup.Option) (Telemetry, error) {
	return Telemetry{
		Tracer:   tracenoop.NewTracerProvider().Tracer("noop"),
		Meter:    metricnoop.NewMeterProvider().Meter("noop"),
		Shutdown: func(context.Context) error { return nil },
	}, nil
}

// setupGuardOnly prepares an observed session with no telemetry export, for a
// run whose telemetry is off (no endpoint, setup failure) but which carries
// --override-input. Any error means the caller fails open as before.
func (r *runner) setupGuardOnly(opts config.Options, cmdPath string) (*session, error) {
	rr := *r
	rr.Deps.NewTelemetry = noopTelemetry
	return rr.setup(opts, config.Resolved{Endpoint: "guard-only"}, cmdPath)
}
