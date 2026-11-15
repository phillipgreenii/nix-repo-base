//go:build realbin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestIntegration_Version(t *testing.T) {
	out, err := exec.CommandContext(context.Background(), pnBinary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "20260531-test") {
		t.Errorf("expected version output, got %q", string(out))
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

	cmd := exec.CommandContext(context.Background(), pnBinary, "workspace", "status")
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
