//go:build smoke

// Package smoke contains end-to-end scenario tests for the pn binary.
// Each scenario is a directory under scenarios/ and is driven by the
// smoke_test.go harness.
package smoke

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitDirEnvVars is the GIT_DIR-location family that git's own repo discovery
// consults BEFORE -C, cmd.Dir, or an explicit path argument (pg2-kersl,
// mechanism proven in pg2-67h4y's design field). It is the single list for this
// package: smoke_test.go's TestMain unsets it process-wide, and buildScrubbedEnv
// strips it from every environment it hands to a subprocess.
//
// The per-call strip in buildScrubbedEnv is NOT redundant with TestMain's
// unset. TestMain only closes the channel where a variable is present BEFORE
// the test binary starts (e.g. exported by a pre-commit hook running in a
// linked worktree). It cannot close one reintroduced AFTER TestMain, such as by
// t.Setenv, and buildScrubbedEnv reads a live os.Environ() at call time.
// Every scenario's setup.sh (44 of them) inherits that environment and runs
// bare `git init`/`git clone`, which is why the strip lives here (see
// README.md, "Why setup.sh is exempt from the hermetic-git-fixture rule").
var gitDirEnvVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CEILING_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OBJECT_DIRECTORY",
}

// buildScrubbedEnv builds a deterministic, isolation-safe environment slice
// for subprocess invocations. It starts from os.Environ() and overrides or
// removes env vars that would cause non-determinism or cross-test pollution.
//
// The scrubbed env includes:
//   - HOME          → <tempHome> (a fresh dir under t.TempDir())
//   - XDG_CONFIG_HOME → <tempHome>/xdg
//   - XDG_DATA_HOME   → <tempHome>/data
//   - XDG_STATE_HOME  → <tempHome>/state
//   - GIT_CONFIG_GLOBAL → /dev/null
//   - GIT_CONFIG_SYSTEM → /dev/null
//   - LC_ALL=C, TZ=UTC
//   - GIT_SSH_COMMAND (BatchMode, StrictHostKeyChecking=accept-new)
//   - GIT_AUTHOR_NAME/EMAIL, GIT_COMMITTER_NAME/EMAIL (fixed values)
//   - PN_WORKSPACE_ROOT → wsRoot (the scenario temp dir)
//   - the GIT_DIR family (gitDirEnvVars) is REMOVED on every call, so a variable
//     set after TestMain (t.Setenv) still cannot reach a scenario's setup.sh or pn
//   - OTEL_*, TRACEPARENT, TRACESTATE are REMOVED, so pn never exports telemetry
//     or inherits a trace parent from the developer shell; with HOME pointed at a
//     temp dir the real ~/.config/pn/telemetry.toml is never read either
//   - PG_NIX_LOG_DISABLE and PN_TRACE_HINT are emptied (inert) for the same reason
//
// This approach is safe for t.Parallel() because it does NOT mutate the
// process environment — it builds a new env slice passed per-subprocess.
func buildScrubbedEnv(t *testing.T, wsRoot string) []string {
	t.Helper()
	tempHome := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(tempHome, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create temp home: %v", err)
	}
	xdgConfig := filepath.Join(tempHome, "xdg")
	if err := os.MkdirAll(xdgConfig, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create xdg config: %v", err)
	}
	xdgState := filepath.Join(tempHome, "state")
	if err := os.MkdirAll(xdgState, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create xdg state: %v", err)
	}
	xdgData := filepath.Join(tempHome, "data")
	if err := os.MkdirAll(xdgData, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create xdg data: %v", err)
	}
	ulLib := filepath.Join(tempHome, "ullib")
	if err := os.MkdirAll(ulLib, 0o755); err != nil {
		t.Fatalf("buildScrubbedEnv: create ullib: %v", err)
	}

	// The fake store S42 validates against must be spelled the way the scenario's
	// setup.sh ($PWD, physical) spells it: on macOS t.TempDir is under the
	// /var -> /private/var symlink.
	physRoot := wsRoot
	if p, err := filepath.EvalSymlinks(wsRoot); err == nil {
		physRoot = p
	}

	// Override map: keys to override or set.
	overrides := map[string]string{
		"HOME":                tempHome,
		"XDG_CONFIG_HOME":     xdgConfig,
		"XDG_DATA_HOME":       xdgData,
		"XDG_STATE_HOME":      xdgState,
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"LC_ALL":              "C",
		"TZ":                  "UTC",
		"GIT_SSH_COMMAND":     "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		"GIT_AUTHOR_NAME":     "pn-smoke-test",
		"GIT_AUTHOR_EMAIL":    "pn-smoke@test.invalid",
		"GIT_COMMITTER_NAME":  "pn-smoke-test",
		"GIT_COMMITTER_EMAIL": "pn-smoke@test.invalid",
		"PN_WORKSPACE_ROOT":   wsRoot,
		"UL_LIB_DIR":          ulLib,
		"PG_NIX_LOG_DISABLE":  "",
		"PN_TRACE_HINT":       "",
		// Read only by a pn built with -tags smokeseams (scenario S42); ignored by
		// every other binary.
		"PN_SMOKE_STORE_DIR": filepath.Join(physRoot, "store"),
	}

	gitDirEnvKeys := make(map[string]bool, len(gitDirEnvVars))
	for _, k := range gitDirEnvVars {
		gitDirEnvKeys[k] = true
	}

	// Build a new env slice from os.Environ(), replacing overridden keys.
	applied := make(map[string]bool, len(overrides))
	var env []string
	for _, kv := range os.Environ() {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			env = append(env, kv)
			continue
		}
		key := kv[:idx]
		if isTelemetryEnvKey(key) || gitDirEnvKeys[key] {
			continue
		}
		if val, ok := overrides[key]; ok {
			env = append(env, fmt.Sprintf("%s=%s", key, val))
			applied[key] = true
		} else {
			env = append(env, kv)
		}
	}
	// Append any overrides not already in the original env.
	for k, v := range overrides {
		if !applied[k] {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return env
}

// isTelemetryEnvKey reports whether key is an OpenTelemetry / trace-context
// variable that must not leak from the developer's shell into a smoke run.
func isTelemetryEnvKey(key string) bool {
	return strings.HasPrefix(key, "OTEL_") || key == "TRACEPARENT" || key == "TRACESTATE"
}
