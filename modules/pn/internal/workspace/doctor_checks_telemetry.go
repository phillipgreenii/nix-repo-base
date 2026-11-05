// internal/workspace/doctor_checks_telemetry.go
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// telemetryCheckID is the doctor check id for the telemetry wiring.
const telemetryCheckID = "telemetry"

// wrapperCheckTimeout bounds `pg-nix-log-wrapped --check`: the wrapper probes
// the collector, and a doctor run must not hang on a dead endpoint.
const wrapperCheckTimeout = 15 * time.Second

// checkTelemetry verifies the telemetry wiring (ADR 0028) when telemetry is
// ON: it runs the wrapper's own `--check` (resolved endpoint, collector
// reachability, log-directory writability) so a silently fail-open
// misconfiguration — the wrapper is deliberately silent when it fails open —
// is detectable.
//
// When telemetry is OFF (telemetry.toml `enabled = false`, a force-off flag or
// variable, or no endpoint resolved) the check produces NO finding: a machine
// without OTel sees doctor output identical to before. "On" means configured
// on: a collector that pn's start-up probe found unreachable (which turns
// telemetry off for that run) is still reported here, because the system is
// configured to emit and the collector is down. So is `enabled = true` with no
// endpoint anywhere. Findings are warnings, never errors: telemetry is
// optional and a broken collector must not fail workspace health.
func (ws *Workspace) checkTelemetry(ctx context.Context, env *doctorEnv) []Finding {
	res := env.telemetry
	if res == nil {
		return nil
	}
	warn := func(msg, manual string) []Finding {
		return []Finding{{
			CheckID: telemetryCheckID, Severity: SevWarning,
			Message: msg, Manual: manual,
		}}
	}
	if !res.Enabled {
		switch {
		case res.ProbeErr != nil:
			return warn(
				fmt.Sprintf("telemetry is enabled (endpoint %s from %s) but the collector is unreachable: %v", res.ProbeEndpoint, res.ProbeSource, res.ProbeErr),
				"start the observability stack, or set phillipgreenii.pn.telemetry.enable = false (telemetry.toml enabled = false) and apply",
			)
		case res.Forced == "" && res.FileEnabled != nil && *res.FileEnabled:
			return warn(
				"telemetry.toml has enabled = true but no endpoint resolves; telemetry stays off",
				"set phillipgreenii.pn.telemetry.endpoint (or OTEL_EXPORTER_OTLP_ENDPOINT) and apply, or set enable = false",
			)
		}
		return nil
	}

	wrapper := res.WrapperPath
	if wrapper == "" {
		return warn(
			fmt.Sprintf("telemetry is enabled (endpoint %s from %s) but no wrapper_path is configured; pn will not wrap nix runs", res.Endpoint, res.Source),
			"set phillipgreenii.pn.telemetry.wrapperPath (default: the pg-nix-log-wrapped package) and apply",
		)
	}
	if _, err := os.Stat(wrapper); err != nil {
		return warn(
			fmt.Sprintf("telemetry is enabled but the wrapper %s is not usable: %v", wrapper, err),
			"pn workspace apply  # reinstalls the pg-nix-log-wrapped package; or set phillipgreenii.pn.telemetry.enable = false",
		)
	}

	wc := env.wrapperCheckResult(ctx, ws, wrapper)
	if wc.telemetryFailed() {
		detail := strings.TrimSpace(wc.telemetryText())
		switch {
		case wc.err != nil && !errors.Is(wc.err, context.DeadlineExceeded):
			detail = strings.TrimSpace(wc.err.Error() + "\n" + detail)
		case errors.Is(wc.err, context.DeadlineExceeded):
			detail = "timed out after " + wrapperCheckTimeout.String()
		}
		return warn(
			fmt.Sprintf("%s --check failed (exit %d): %s", wrapper, wc.out.ExitCode, firstLines(detail, 4)),
			wrapper+" --check",
		)
	}
	return nil
}

// wrapperCheck is one run of `pg-nix-log-wrapped --check`, shared by the
// telemetry and shim checks so doctor probes the collector once.
type wrapperCheck struct {
	out exec.Result
	err error
}

// wrapperCheckResult runs the wrapper's --check at most once per doctor run.
func (env *doctorEnv) wrapperCheckResult(ctx context.Context, ws *Workspace, wrapper string) *wrapperCheck {
	if env.wrapperCheck != nil {
		return env.wrapperCheck
	}
	cctx, cancel := context.WithTimeout(ctx, wrapperCheckTimeout)
	defer cancel()
	out, err := ws.runner.Run(cctx, wrapper, []string{"--check"}, exec.RunOptions{})
	env.wrapperCheck = &wrapperCheck{out: out, err: err}
	return env.wrapperCheck
}

// statusLine is the wrapper's final "status: telemetry=ok shim=fail" line; ""
// from an older wrapper that does not print it.
func (wc *wrapperCheck) statusLine() string {
	for _, ln := range strings.Split(string(wc.out.Stdout), "\n") {
		if strings.HasPrefix(ln, "status: ") {
			return ln
		}
	}
	return ""
}

// telemetryFailed says whether the telemetry half of --check failed. The
// wrapper now exits non-zero for a shim failure alone, which the shim check
// reports; with a status line only its telemetry verdict counts here, and an
// older wrapper (no status line) keeps the plain exit-code rule.
func (wc *wrapperCheck) telemetryFailed() bool {
	if st := wc.statusLine(); st != "" && !errors.Is(wc.err, context.DeadlineExceeded) {
		return strings.Contains(st, "telemetry=fail")
	}
	return wc.err != nil || wc.out.ExitCode != 0
}

// telemetryText is the --check output without the shim and status lines, so a
// telemetry warning reads as it did before the shim lines existed.
func (wc *wrapperCheck) telemetryText() string {
	var keep []string
	for _, ln := range strings.Split(string(wc.out.Stdout), "\n") {
		if !strings.HasPrefix(ln, "shim: ") && !strings.HasPrefix(ln, "status: ") {
			keep = append(keep, ln)
		}
	}
	return strings.Join(keep, "\n") + "\n" + string(wc.out.Stderr)
}

// firstLines returns at most n non-empty lines of s joined with "; ".
func firstLines(s string, n int) string {
	var keep []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			keep = append(keep, ln)
			if len(keep) == n {
				break
			}
		}
	}
	return strings.Join(keep, "; ")
}
