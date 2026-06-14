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

// buildScrubbedEnv builds a deterministic, isolation-safe environment slice
// for subprocess invocations. It starts from os.Environ() and overrides or
// removes env vars that would cause non-determinism or cross-test pollution.
//
// The scrubbed env includes:
//   - HOME          → <tempHome> (a fresh dir under t.TempDir())
//   - XDG_CONFIG_HOME → <tempHome>/xdg
//   - XDG_STATE_HOME  → <tempHome>/state
//   - GIT_CONFIG_GLOBAL → /dev/null
//   - GIT_CONFIG_SYSTEM → /dev/null
//   - LC_ALL=C, TZ=UTC
//   - GIT_SSH_COMMAND (BatchMode, StrictHostKeyChecking=accept-new)
//   - GIT_AUTHOR_NAME/EMAIL, GIT_COMMITTER_NAME/EMAIL (fixed values)
//   - PN_WORKSPACE_ROOT → wsRoot (the scenario temp dir)
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

	// Override map: keys to override or set.
	overrides := map[string]string{
		"HOME":             tempHome,
		"XDG_CONFIG_HOME": xdgConfig,
		"XDG_STATE_HOME":  xdgState,
		"GIT_CONFIG_GLOBAL": "/dev/null",
		"GIT_CONFIG_SYSTEM": "/dev/null",
		"LC_ALL":            "C",
		"TZ":                "UTC",
		"GIT_SSH_COMMAND":   "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		"GIT_AUTHOR_NAME":   "pn-smoke-test",
		"GIT_AUTHOR_EMAIL":  "pn-smoke@test.invalid",
		"GIT_COMMITTER_NAME":  "pn-smoke-test",
		"GIT_COMMITTER_EMAIL": "pn-smoke@test.invalid",
		"PN_WORKSPACE_ROOT":   wsRoot,
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
