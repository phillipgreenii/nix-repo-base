//go:build realnix || realbin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pnBinary is the pn binary built once (with a version stamp) for the suites that
// drive the real binary. It only exists under the build tags that need it, so the
// default unit gate pays for no go build.
var pnBinary string

func runIntegrationTests(m *testing.M) int {
	// Build the pn binary once for all integration tests.
	tmp, err := os.MkdirTemp("", "pn-binary-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	pnBinary = filepath.Join(tmp, "pn")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-ldflags", "-X main.Version=20260531-test", "-o", pnBinary, ".")
	cmd.Env = pnEnv()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
	return m.Run()
}
