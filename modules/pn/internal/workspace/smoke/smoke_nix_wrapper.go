//go:build smoke

package smoke

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// spoolDirPlaceholder is the token a scenario's command.txt uses for the spool
// directory pn is given with --otel-spool-dir.
const spoolDirPlaceholder = "@SPOOL_DIR@"

// expandSpoolDir replaces spoolDirPlaceholder in lines with an absolute path
// under a fresh temporary directory. The directory is not created: pn creates
// the spool on first write, and S38-S40 only inspect the argv pn hands the
// wrapper. When no line uses the placeholder lines is returned unchanged.
func expandSpoolDir(t *testing.T, lines []string) []string {
	t.Helper()
	uses := false
	for _, l := range lines {
		if strings.Contains(l, spoolDirPlaceholder) {
			uses = true
			break
		}
	}
	if !uses {
		return lines
	}
	dir := filepath.Join(t.TempDir(), "otel-spool")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, spoolDirPlaceholder, dir)
	}
	return out
}

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
	// wrapper[3] is the spool directory pn was given (a temporary path only
	// known at run time, see expandSpoolDir); pn never passes --otlp-endpoint.
	if wrapper[2] != "--otel-spool-dir" || !filepath.IsAbs(wrapper[3]) || filepath.Base(wrapper[3]) != "otel-spool" {
		t.Errorf("S38: wrapper argv has no absolute --otel-spool-dir after the traceparent: %q", wrapper)
	}
	if slices.Contains(wrapper, "--otlp-endpoint") {
		t.Errorf("S38: pn passed the retired --otlp-endpoint: %q", wrapper)
	}
	wantTail := []string{
		"--log-dir", filepath.Join(envValue(env, "XDG_STATE_HOME"), "pn", "nix-logs"),
		"--",
		"darwin-rebuild", "build",
	}
	if got := wrapper[4:]; !reflect.DeepEqual(got, wantTail) {
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

// assertS48SpoolDefaultPath: with telemetry enabled and no other setting, one
// pn run leaves valid OTLP-JSON trace and metric files under
// $XDG_STATE_HOME/otel-spool and prints nothing on stderr; the same run with
// --no-telemetry writes nothing; a leftover telemetry.toml endpoint key prints
// exactly one deprecation notice and nothing else.
func assertS48SpoolDefaultPath(t *testing.T, wsRoot, pnBin string, env []string, last scenarioResult) {
	t.Helper()
	if last.ExitCode != 0 || len(last.Stderr) != 0 {
		t.Fatalf("S48: enabled run: exit=%d stderr=%q, want 0 and empty", last.ExitCode, last.Stderr)
	}
	spool := filepath.Join(envValue(env, "XDG_STATE_HOME"), "otel-spool")
	for _, sub := range []string{"traces", "metrics"} {
		files, _ := filepath.Glob(filepath.Join(spool, sub, "*.jsonl"))
		if len(files) == 0 {
			t.Fatalf("S48: no spool file under %s/%s", spool, sub)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				var v map[string]any
				if err := json.Unmarshal([]byte(line), &v); err != nil || len(v) == 0 {
					t.Errorf("S48: %s: not a JSON object line: %q (%v)", f, line, err)
				}
			}
		}
	}

	// Disabled: a fresh state directory stays empty and stderr stays empty.
	freshState := t.TempDir()
	off := overrideEnv(env, []string{"XDG_STATE_HOME=" + freshState})
	res := runCommand(t, pnBin, wsRoot, []string{"--no-telemetry", "workspace", "init"}, off)
	if res.ExitCode != 0 || len(res.Stderr) != 0 {
		t.Errorf("S48: disabled run: exit=%d stderr=%q, want 0 and empty", res.ExitCode, res.Stderr)
	}
	if entries, _ := os.ReadDir(freshState); len(entries) != 0 {
		t.Errorf("S48: disabled run wrote under XDG_STATE_HOME: %v", entries)
	}

	// A leftover endpoint key: exactly one notice, still no stray output.
	cfg := filepath.Join(envValue(env, "XDG_CONFIG_HOME"), "pn", "telemetry.toml")
	if err := os.WriteFile(cfg, []byte("enabled = false\nendpoint = \"http://127.0.0.1:1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = runCommand(t, pnBin, wsRoot, []string{"workspace", "init"}, off)
	lines := strings.Split(strings.TrimSpace(string(res.Stderr)), "\n")
	if res.ExitCode != 0 || len(lines) != 1 || !strings.HasPrefix(lines[0], "pn: telemetry: ") || !strings.Contains(lines[0], "endpoint") {
		t.Errorf("S48: leftover endpoint key: exit=%d stderr=%q, want exactly one pn: telemetry: notice", res.ExitCode, res.Stderr)
	}
}
