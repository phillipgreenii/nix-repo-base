// internal/workspace/doctor_checks_shim.go
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// shimCheckID is the doctor check id for the pg-nix-shim wiring.
const shimCheckID = "shim"

// defaultShimRootConfig is where the nix-darwin module writes the root shim
// configuration.
const defaultShimRootConfig = "/etc/pg-nix-shim.json"

// rootLogNote is appended to every root-related finding: a root run's logs
// are mode 0600 under /var/log/pg-nix-log-wrapped, so a non-root doctor can
// neither read them nor see whether root runs were wrapped.
const rootLogNote = "root run logs are mode 0600 and unreadable to a non-root doctor"

// checkShim turns the shim lines of the wrapper's `--check` (the same run the
// telemetry check uses) into findings. It is silent when telemetry is off, the
// wrapper is unusable or its --check cannot be run (the telemetry check
// reports those), and when no shim module is in use (no nix or darwin-rebuild
// shim on PATH and no root configuration), so a machine without the shim modules
// sees the same doctor output as before. Findings are warnings, never errors.
//
// A silent fail-open is not visible here: a passthrough execs the real binary
// before any telemetry exists. This check finds the installable causes (shim
// not first on PATH, root configuration missing or a copy, endpoints that
// disagree).
func (ws *Workspace) checkShim(ctx context.Context, env *doctorEnv) []Finding {
	res := env.telemetry
	if res == nil || !res.Enabled || res.WrapperPath == "" {
		return nil
	}
	if _, err := os.Stat(res.WrapperPath); err != nil {
		return nil
	}
	wc := env.wrapperCheckResult(ctx, ws, res.WrapperPath)
	if wc.err != nil && wc.out.Stdout == nil {
		return nil
	}
	lines := shimLines(string(wc.out.Stdout))
	if len(lines) == 0 {
		return nil // an older wrapper without shim detection
	}

	notShim := []string{}
	anyShim, rootState, rootRun := false, "", ""
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, ": shim at "):
			anyShim = true
		case strings.Contains(ln, ": NOT a shim:"):
			notShim = append(notShim, ln)
		case strings.HasPrefix(ln, "root-config: "):
			rootState = strings.TrimPrefix(ln, "root-config: ")
		case strings.HasPrefix(ln, "root-run: "):
			rootRun = strings.TrimPrefix(ln, "root-run: ")
		}
	}
	rootAbsent := strings.HasPrefix(rootState, "absent")
	if !anyShim && rootAbsent {
		return nil // the shim modules are not in use here
	}

	warn := func(msg, manual string) Finding {
		return Finding{CheckID: shimCheckID, Severity: SevWarning, Message: msg, Manual: manual}
	}
	var out []Finding
	for _, ln := range notShim {
		out = append(out, warn(
			"pg-nix-shim: "+ln+"; calls to it are not wrapped",
			res.WrapperPath+" --check  # check PATH order; enable phillipgreenii.pg-nix-shim and apply",
		))
	}
	if strings.Contains(rootState, "(not a store symlink)") {
		out = append(out, warn(
			"root config not a store symlink: "+rootState+"; root runs pass through unwrapped ("+rootLogNote+")",
			"enable the nix-darwin pg-nix-shim module and apply; do not copy the file into /etc",
		))
	} else if rootAbsent && anyShim {
		out = append(out, warn(
			"pg-nix-shim: no root configuration ("+rootState+"); root runs (sudo nix, sudo darwin-rebuild) pass through unwrapped ("+rootLogNote+")",
			"enable the nix-darwin pg-nix-shim module (set rootEndpoint) and apply",
		))
	}
	if f, ok := ws.shimEndpointMismatch(env, rootState, rootRun); ok {
		out = append(out, f)
	}
	return out
}

// shimLines returns the wrapper's "shim: ..." lines with the prefix removed.
func shimLines(stdout string) []string {
	var out []string
	for _, ln := range strings.Split(stdout, "\n") {
		if rest, ok := strings.CutPrefix(ln, "shim: "); ok {
			out = append(out, strings.TrimSpace(rest))
		}
	}
	return out
}

// shimEndpointMismatch compares the home-manager endpoint (telemetry.toml)
// with rootEndpoint in the root configuration, when both are readable.
func (ws *Workspace) shimEndpointMismatch(env *doctorEnv, rootState, rootRun string) (Finding, bool) {
	res := env.telemetry
	if !strings.Contains(rootState, "(store regular file)") || res.Source != "file" || res.Endpoint == "" {
		return Finding{}, false
	}
	path := env.shimRootConfig
	if path == "" {
		path = defaultShimRootConfig
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Finding{}, false
	}
	var rc struct {
		RootEndpoint string `json:"rootEndpoint"`
	}
	if json.Unmarshal(b, &rc) != nil || rc.RootEndpoint == "" || rc.RootEndpoint == res.Endpoint {
		return Finding{}, false
	}
	return Finding{
		CheckID: shimCheckID, Severity: SevWarning,
		Message: fmt.Sprintf("pg-nix-shim: the home-manager telemetry endpoint (%s, telemetry.toml) differs from the nix-darwin rootEndpoint (%s, %s); user and root runs are sent to different collectors (%s)",
			res.Endpoint, rc.RootEndpoint, path, rootLogNote),
		Manual: "set phillipgreenii.pn.telemetry.endpoint and the nix-darwin pg-nix-shim rootEndpoint from one value, then apply",
	}, true
}
