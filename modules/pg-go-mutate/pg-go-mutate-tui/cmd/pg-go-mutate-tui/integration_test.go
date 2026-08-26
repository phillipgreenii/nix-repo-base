package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/queue"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWiredProgramDiscoversAndAnalysesAFileEndToEnd(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkgDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "a.go"), []byte("package pkg\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "a_test.go"), []byte(
		"package pkg\nimport \"testing\"\nfunc TestAdd(t *testing.T) { if Add(2,3)!=5 { t.Fatal(\"bad\") } }\n",
	), 0o644); err != nil {
		t.Fatalf("write a_test.go: %v", err)
	}

	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	app, err := newApp(appOptions{
		Root:        root,
		Concurrency: 1,
		StubRunFunc: func(file string) (string, string, error) { return "done", "", nil }, // avoids a real pg-go-mutate/gomu dependency in this unit test
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app.runHeadless(ctx) // drives discovery + one refill + the worker pool with no TUI attached

	records, err := app.ledger.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least one file to be discovered, queued, and recorded")
	}
}

func TestNewTUIModelImplementsTeaModel(t *testing.T) {
	var _ tea.Model = newTUIModel(&app{q: queue.NewQueue(1, 10, time.Minute)})
}

// TestNewAppErrorsWhenNeitherXDGNorHOMEIsSet guards against a silent
// CWD-relative fallback: if XDG_CONFIG_HOME/XDG_STATE_HOME and HOME are all
// unset, newApp must fail rather than resolve config/state paths relative
// to whatever directory the process happens to be run from.
func TestNewAppErrorsWhenNeitherXDGNorHOMEIsSet(t *testing.T) {
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "HOME"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("unset %s: %v", v, err)
		}
	}
	if _, err := newApp(appOptions{Root: t.TempDir()}); err == nil {
		t.Fatal("expected an error when neither XDG_CONFIG_HOME/XDG_STATE_HOME nor HOME is set")
	}
}

// TestMetricsAddrIsLoopbackOnly guards against re-widening the /metrics
// bind address to all interfaces.
func TestMetricsAddrIsLoopbackOnly(t *testing.T) {
	host, _, err := net.SplitHostPort(metricsAddr)
	if err != nil {
		t.Fatalf("metricsAddr %q: %v", metricsAddr, err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("metricsAddr %q binds host %q, want loopback only", metricsAddr, host)
	}
}
