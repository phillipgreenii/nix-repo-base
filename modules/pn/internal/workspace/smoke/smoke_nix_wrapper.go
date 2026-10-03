//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// envValue returns the value of key in an env slice ("" if absent).
func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// readArgvFile reads a stand-in's argv record (one arg per line). ok is false
// when the file does not exist.
func readArgvFile(t *testing.T, path string) (args []string, ok bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), true
}

// traceparentRE is a W3C traceparent: version 00, 32-hex trace id, 16-hex span id, flags.
var traceparentRE = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// assertS38WrapperArgvRewrite: pn ran the fake wrapper with the documented
// flags, `--`, then the command it would have run unwrapped, and the command
// itself received exactly its own argv.
func assertS38WrapperArgvRewrite(t *testing.T, wsRoot string, env []string) {
	t.Helper()
	wrapper, ok := readArgvFile(t, filepath.Join(wsRoot, "wrapper.argv"))
	if !ok {
		t.Fatal("S38: the wrapper never ran (wrapper.argv missing)")
	}
	if len(wrapper) != 9 {
		t.Fatalf("S38: wrapper argv = %q, want 9 args", wrapper)
	}
	if wrapper[0] != "--traceparent" || !traceparentRE.MatchString(wrapper[1]) {
		t.Errorf("S38: wrapper argv does not start with a valid --traceparent: %q", wrapper)
	}
	wantTail := []string{
		"--otlp-endpoint", "http://127.0.0.1:9",
		"--log-dir", filepath.Join(envValue(env, "XDG_STATE_HOME"), "pn", "nix-logs"),
		"--",
		"darwin-rebuild", "build",
	}
	if got := wrapper[2:]; !reflect.DeepEqual(got, wantTail) {
		t.Errorf("S38: wrapper argv tail = %q, want %q", got, wantTail)
	}
	darwin, ok := readArgvFile(t, filepath.Join(wsRoot, "darwin.argv"))
	if !ok || !reflect.DeepEqual(darwin, []string{"build"}) {
		t.Errorf("S38: darwin-rebuild argv = %q (ran=%v), want [build]", darwin, ok)
	}
}

// assertUnwrappedBuild: the build ran darwin-rebuild directly with its own argv
// and the wrapper was never invoked (S39: wrapper missing, S40: telemetry off).
func assertUnwrappedBuild(t *testing.T, scenario, wsRoot string) {
	t.Helper()
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "wrapper.argv")); ok {
		t.Errorf("%s: the wrapper ran but the build must run unwrapped", scenario)
	}
	darwin, ok := readArgvFile(t, filepath.Join(wsRoot, "darwin.argv"))
	if !ok || !reflect.DeepEqual(darwin, []string{"build"}) {
		t.Errorf("%s: darwin-rebuild argv = %q (ran=%v), want [build]", scenario, darwin, ok)
	}
}
