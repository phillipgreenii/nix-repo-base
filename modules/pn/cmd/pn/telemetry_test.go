package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// End to end with the real binary: a SIGTERM-killed pn still flushes its
// spans to the collector, within the bounded timeout.
func TestIntegration_SIGTERMStillFlushes(t *testing.T) {
	var traces atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			traces.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The workspace's only repo is the hermetic fixture repo (x/gittest); the
	// workspace root is the fixture tree that contains it, at <root>/repo.
	fixture := gittest.New(t, gitfixture.RepoOptions{Suite: "pn-telemetry"})
	root := filepath.Dir(fixture.Dir)
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), []byte("[repos.repo]\nurl = \"github:o/repo\"\n[[repos.repo.hooks]]\nwhen = [\"post-clone\"]\nrun = [\"{nix_run install-pre-commit-hooks}\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake `pg-hooks` (what pre-commit-check runs for a repo that declares the
	// install-pre-commit-hooks hook) that execs sleep so SIGTERM reaches the sleeping process itself
	// and records that it started.
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	script := "#!/bin/sh\ntouch " + started + "\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(bin, "pg-hooks"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	cmd := exec.Command(pnBinary, "workspace", "pre-commit-check")
	cmd.Dir = root
	cmd.Env = pnEnv(
		"PATH="+bin+":"+os.Getenv("PATH"),
		"HOME="+home,
		"OTEL_EXPORTER_OTLP_ENDPOINT="+srv.URL,
		"PN_WORKSPACE_ROOT="+root,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("fake pg-hooks never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("pn exited 0 after SIGTERM mid-command, want non-zero")
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("pn did not exit within 30s of SIGTERM")
	}
	if traces.Load() == 0 {
		t.Error("collector received no spans from the SIGTERM-killed pn")
	}
}
