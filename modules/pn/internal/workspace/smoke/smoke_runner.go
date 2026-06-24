//go:build smoke

package smoke

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scenarioResult holds the captured output of one command invocation.
type scenarioResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// runCommand executes a pn binary invocation with the given args inside wsRoot.
// env is the per-scenario scrubbed environment (from buildScrubbedEnv).
func runCommand(t *testing.T, pnBin, wsRoot string, args []string, env []string) scenarioResult {
	t.Helper()
	cmd := exec.Command(pnBin, args...)
	cmd.Dir = wsRoot
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			// Non-exit error (e.g., binary not found) — treat as exit 127.
			exitCode = 127
		}
	}
	return scenarioResult{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
}

// runSetupScript runs setup.sh inside wsRoot using bash.
// env is the per-scenario scrubbed environment.
func runSetupScript(t *testing.T, setupPath, wsRoot string, env []string) error {
	t.Helper()
	cmd := exec.Command("bash", setupPath)
	cmd.Dir = wsRoot
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup.sh failed: %v\nOutput:\n%s", err, out.String())
	}
	return nil
}

// buildPNBinary builds the pn binary from source once. The binary is placed
// in a process-lifetime temp directory (os.MkdirTemp, NOT t.TempDir()) so it
// persists for the entire test run regardless of which test's goroutine finishes
// first. The caller is responsible for cleanup (done in TestMain).
//
// Build is performed with CGO_ENABLED=0 and the module root inferred by
// walking upward from the test file to find go.mod.
func buildPNBinary(t *testing.T, moduleRoot string) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pn-smoke-bin-*")
	if err != nil {
		t.Fatalf("build pn binary: create temp dir: %v", err)
	}
	binPath := filepath.Join(tmpDir, "pn")
	// Register cleanup so the temp dir is removed after all tests finish.
	// This is called from TestMain via a global cleanup hook.
	pnBinTmpDirs = append(pnBinTmpDirs, tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 120000000000) // 2 minutes
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build",
		"-ldflags", "-X main.Version=smoke-test",
		"-o", binPath, "./cmd/pn")
	cmd.Dir = moduleRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("build pn binary failed: %v\nOutput:\n%s", err, out.String())
	}
	return binPath
}

// pnBinTmpDirs holds temp dirs created for the pn binary; cleaned up in TestMain.
var pnBinTmpDirs []string

// copyFile copies src to dst. Creates dst with mode 0o644.
// Arg order matches workspace.copyFile (src, dst) to avoid a cross-package foot-gun.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// sha256File returns the hex-encoded SHA-256 of a file's contents.
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// readLines reads a file and returns non-empty, non-comment lines.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

// assertSubstrings reads an assertion file (one substring per line) and
// verifies each appears in actual. Pass prefix="stdout" or "stderr" for
// error messages.
func assertSubstrings(t *testing.T, scenarioName, prefix, assertFile string, actual []byte) {
	t.Helper()
	lines, err := readLines(assertFile)
	if err != nil {
		t.Errorf("%s: read %s: %v", scenarioName, assertFile, err)
		return
	}
	for _, want := range lines {
		if !strings.Contains(string(actual), want) {
			t.Errorf("%s: %s missing substring %q\ngot:\n%s", scenarioName, prefix, want, string(actual))
		}
	}
}

// assertExitCode reads expected_exit.txt (default 0) and checks the result.
func assertExitCode(t *testing.T, scenarioName, exitFile string, got int) {
	t.Helper()
	want := 0
	if exitFile != "" {
		data, err := os.ReadFile(exitFile)
		if err == nil {
			line := strings.TrimSpace(string(data))
			if line != "" {
				n, err2 := strconv.Atoi(line)
				if err2 != nil {
					t.Errorf("%s: invalid expected_exit.txt content %q: %v", scenarioName, line, err2)
					return
				}
				want = n
			}
		}
	}
	if got != want {
		t.Errorf("%s: exit code = %d, want %d", scenarioName, got, want)
	}
}

// assertJSONSubset reads expected.json and verifies it is a subset of the
// actual pn-workspace.lock.json. If _strict: true is set in expected.json,
// performs a strict equality check.
func assertJSONSubset(t *testing.T, scenarioName, expectedFile, lockFile string) {
	t.Helper()
	expectedData, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Errorf("%s: read %s: %v", scenarioName, expectedFile, err)
		return
	}
	lockData, err := os.ReadFile(lockFile)
	if err != nil {
		t.Errorf("%s: read lock file %s: %v", scenarioName, lockFile, err)
		return
	}

	var expected, actual map[string]interface{}
	if err := json.Unmarshal(expectedData, &expected); err != nil {
		t.Errorf("%s: parse expected.json: %v", scenarioName, err)
		return
	}
	if err := json.Unmarshal(lockData, &actual); err != nil {
		t.Errorf("%s: parse lock file: %v", scenarioName, err)
		return
	}

	strict, _ := expected["_strict"].(bool)
	delete(expected, "_strict")

	if strict {
		// Strict: all keys in actual must appear in expected (and vice versa).
		actualNorm, err := json.Marshal(actual)
		if err != nil {
			t.Errorf("%s: re-marshal actual: %v", scenarioName, err)
			return
		}
		expectedNorm, err := json.Marshal(expected)
		if err != nil {
			t.Errorf("%s: re-marshal expected: %v", scenarioName, err)
			return
		}
		if string(actualNorm) != string(expectedNorm) {
			t.Errorf("%s: strict JSON mismatch\ngot:  %s\nwant: %s", scenarioName, actualNorm, expectedNorm)
		}
		return
	}

	// Subset: every key in expected must match actual.
	if err := checkSubset(scenarioName, expected, actual, t); err != nil {
		t.Errorf("%s: JSON subset check failed: %v", scenarioName, err)
	}
}

// checkSubset verifies that every key in expected exists in actual with the
// same (deep-equal) value, recursively for nested maps. Arrays are compared
// element-wise with JSON normalization.
func checkSubset(scenarioName string, expected, actual map[string]interface{}, t *testing.T) error {
	t.Helper()
	for key, expVal := range expected {
		actVal, ok := actual[key]
		if !ok {
			return fmt.Errorf("key %q missing from actual lock", key)
		}
		if !subsetMatch(scenarioName, key, expVal, actVal, t) {
			// subsetMatch already called t.Errorf
		}
	}
	return nil
}

// subsetMatch recursively checks that expVal is a subset of actVal.
// For maps: every key in expVal must be in actVal with matching value (subset).
// For everything else: JSON-normalize and compare exactly.
// Returns true if match (or if mismatch was already reported via t.Errorf).
func subsetMatch(scenarioName, path string, expVal, actVal interface{}, t *testing.T) bool {
	t.Helper()
	expMap, expIsMap := expVal.(map[string]interface{})
	actMap, actIsMap := actVal.(map[string]interface{})

	if expIsMap && actIsMap {
		for k, ev := range expMap {
			av, ok := actMap[k]
			if !ok {
				t.Errorf("%s: path %s.%s: missing from actual", scenarioName, path, k)
				return false
			}
			subsetMatch(scenarioName, path+"."+k, ev, av, t)
		}
		return true
	}

	// Non-map: exact JSON comparison.
	expJSON, _ := json.Marshal(expVal)
	actJSON, _ := json.Marshal(actVal)
	if string(expJSON) != string(actJSON) {
		t.Errorf("%s: path %s: got %s, want %s", scenarioName, path, actJSON, expJSON)
		return false
	}
	return true
}

// captureFileHashes returns the SHA-256 hash of each named file in dir.
// Files that don't exist are not included in the map.
func captureFileHashes(dir string, names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, name := range names {
		h, err := sha256File(filepath.Join(dir, name))
		if err == nil {
			out[name] = h
		}
	}
	return out
}

// parseCommandLine splits a command.txt line into args. Handles simple
// quoting (double and single quotes) and shell-style splitting.
// This is intentionally simple; scenario commands should not need complex quoting.
func parseCommandLine(line string) []string {
	var args []string
	var cur strings.Builder
	inDouble := false
	inSingle := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == ' ' && !inDouble && !inSingle:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}
