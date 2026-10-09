package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

var pnBinary string

func TestMain(m *testing.M) {
	// os.Exit skips deferred functions, so run the suite in a helper whose
	// deferred temp-dir cleanup actually executes (on both the normal return
	// and a setup panic) before we exit. Previously the cleanup deferred here
	// never ran, leaking a pn-binary-* temp dir per run (bead pg2-xtmil).
	os.Exit(runIntegrationTests(m))
}

// pnEnv is the environment for a subprocess that runs the pn binary (or builds
// it): the test process's environment with every GIT_* variable dropped and the
// global/system git config redirected to /dev/null. The test fixtures
// (x/gittest) are hermetic by construction, but the pn binary runs its own git
// subprocesses and would otherwise inherit what a git hook exports (GIT_DIR and
// friends from the commit in progress, which discovery consults FIRST) and the
// developer's ~/.gitconfig (a global core.fsmonitor=true makes `git status`
// contend for a possibly wedged fsmonitor daemon and hang the suite, pg2-39rz2).
// Scoping this to the subprocess, rather than mutating the test process, keeps
// the fixtures honest: they must not depend on a pre-scrubbed environment.
func pnEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	return append(env, extra...)
}

func runIntegrationTests(m *testing.M) int {
	// Build the pn binary once for all integration tests.
	tmp, err := os.MkdirTemp("", "pn-binary-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	pnBinary = filepath.Join(tmp, "pn")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.Version=20260531-test", "-o", pnBinary, ".")
	cmd.Env = pnEnv()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
	return m.Run()
}

func TestIntegration_Version(t *testing.T) {
	out, err := exec.Command(pnBinary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "20260531-test") {
		t.Errorf("expected version output, got %q", string(out))
	}
}

func TestIntegration_RejectsDevVersion(t *testing.T) {
	tmpDir := t.TempDir()
	devBinary := filepath.Join(tmpDir, "pn-dev")
	build := exec.Command("go", "build", "-o", devBinary, ".")
	build.Env = pnEnv()
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(devBinary, "--version").CombinedOutput()
	if err == nil {
		t.Errorf("expected dev binary to fail; got output %q", string(out))
	}
	if !strings.Contains(string(out), "dev") {
		t.Errorf("expected error message to mention dev, got %q", string(out))
	}
}

func TestIntegration_WorkspaceStatus(t *testing.T) {
	// The hermetic fixture repo (x/gittest) is the workspace's only repo; the
	// workspace root is the fixture tree that contains it, at <root>/test-repo.
	fixture := gittest.New(t, gitfixture.RepoOptions{Suite: "pn-integration", Name: "test-repo"})
	workspaceRoot := filepath.Dir(fixture.Dir)
	tomlPath := filepath.Join(workspaceRoot, "pn-workspace.toml")
	if err := os.WriteFile(tomlPath, []byte(`
[repos.test-repo]
url = "github:test/test-repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pnBinary, "workspace", "status")
	cmd.Dir = workspaceRoot
	cmd.Env = pnEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workspace status: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "test-repo") {
		t.Errorf("expected output to mention test-repo, got %q", string(out))
	}
}
