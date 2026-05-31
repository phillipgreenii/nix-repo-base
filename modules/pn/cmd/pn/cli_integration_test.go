package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var pnBinary string

func TestMain(m *testing.M) {
	// Build the pn binary once for all integration tests.
	tmp, err := os.MkdirTemp("", "pn-binary-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	pnBinary = filepath.Join(tmp, "pn")
	cmd := exec.Command("go", "build", "-ldflags", "-X main.Version=20260531-test", "-o", pnBinary, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
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
	if err := exec.Command("go", "build", "-o", devBinary, ".").Run(); err != nil {
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
	workspaceRoot := t.TempDir()
	repoDir := filepath.Join(workspaceRoot, "test-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", repoDir).Run(); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(workspaceRoot, "pn-workspace.toml")
	if err := os.WriteFile(tomlPath, []byte(`
[repos.test-repo]
url = "github:test/test-repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pnBinary, "workspace", "status")
	cmd.Dir = workspaceRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workspace status: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "test-repo") {
		t.Errorf("expected output to mention test-repo, got %q", string(out))
	}
}

func TestIntegration_OSXSubcommandHiddenOnLinux(t *testing.T) {
	if runtimeGOOS == "darwin" {
		t.Skip("osx subcommand IS registered on darwin")
	}
	out, err := exec.Command(pnBinary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help: %v: %s", err, out)
	}
	if strings.Contains(string(out), "osx") {
		t.Errorf("expected --help to NOT mention osx on linux, got %q", string(out))
	}
	out2, err := exec.Command(pnBinary, "osx").CombinedOutput()
	if err == nil {
		t.Errorf("expected pn osx to fail on linux, got output %q", string(out2))
	}
}
