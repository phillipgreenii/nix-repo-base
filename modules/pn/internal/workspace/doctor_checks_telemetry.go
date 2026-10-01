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
// When telemetry is OFF (no endpoint resolved, or forced off) the check
// produces NO finding: a machine without OTel sees doctor output identical to
// before. Findings are warnings, never errors: telemetry is optional and a
// broken collector must not fail workspace health.
func (ws *Workspace) checkTelemetry(ctx context.Context, env *doctorEnv) []Finding {
	res := env.telemetry
	if res == nil || !res.Enabled {
		return nil
	}
	warn := func(msg, manual string) []Finding {
		return []Finding{{
			CheckID: telemetryCheckID, Severity: SevWarning,
			Message: msg, Manual: manual,
		}}
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
			"pn workspace apply  # reinstalls the pg-nix-log-wrapped package; or phillipgreenii.pn.telemetry.enable = false",
		)
	}

	cctx, cancel := context.WithTimeout(ctx, wrapperCheckTimeout)
	defer cancel()
	out, err := ws.runner.Run(cctx, wrapper, []string{"--check"}, exec.RunOptions{})
	if err != nil || out.ExitCode != 0 {
		detail := strings.TrimSpace(string(out.Stdout) + "\n" + string(out.Stderr))
		switch {
		case err != nil && !errors.Is(err, context.DeadlineExceeded):
			detail = strings.TrimSpace(err.Error() + "\n" + detail)
		case errors.Is(err, context.DeadlineExceeded):
			detail = "timed out after " + wrapperCheckTimeout.String()
		}
		return warn(
			fmt.Sprintf("%s --check failed (exit %d): %s", wrapper, out.ExitCode, firstLines(detail, 4)),
			wrapper+" --check",
		)
	}
	return nil
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
