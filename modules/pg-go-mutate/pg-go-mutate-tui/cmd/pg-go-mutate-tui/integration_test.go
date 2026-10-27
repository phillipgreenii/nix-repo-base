package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/beads"
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

// newFixtureApp builds an app over a one-package fixture module, with a stub
// run func so no real pg-go-mutate is needed.
func newFixtureApp(t *testing.T) *app {
	t.Helper()
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkgDir: %v", err)
	}
	files := map[string]string{
		filepath.Join(root, "go.mod"):      "module fixture\n\ngo 1.22\n",
		filepath.Join(pkgDir, "a.go"):      "package pkg\nfunc Add(a, b int) int { return a + b }\n",
		filepath.Join(pkgDir, "a_test.go"): "package pkg\nimport \"testing\"\nfunc TestAdd(t *testing.T) { if Add(2,3)!=5 { t.Fatal(\"bad\") } }\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := newApp(appOptions{
		Root:        root,
		Concurrency: 1,
		StubRunFunc: func(file string) (string, string, error) { return "done", "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestRunHeadlessWaitsForASlowFirstRefillInsteadOfSleeping guards against a
// fixed sleep gating pool start: the first Refill here takes longer than the
// old 1100 ms sleep, and the file must still be discovered, queued and
// recorded because the pool starts only once the first Refill has finished.
func TestRunHeadlessWaitsForASlowFirstRefillInsteadOfSleeping(t *testing.T) {
	a := newFixtureApp(t)
	a.refillLoopFn = func(ctx context.Context, root string) {
		time.Sleep(1500 * time.Millisecond) // a first Refill that misses the old 1100 ms window
		a.refillOnce(root, func(string) time.Time { return time.Time{} })
		a.markFirstRefillDone()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.runHeadless(ctx)

	records, err := a.ledger.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected the file to be recorded even though the first Refill outlasted the old fixed sleep")
	}
}

// TestRunHeadlessStartsPoolAfterBoundedWaitWhenFirstRefillNeverFinishes
// covers the upper bound: a Refill that never finishes must not hang the
// headless run. The wait gives up at firstRefillTimeout and runHeadless
// returns (the pool finds an empty queue and exits).
func TestRunHeadlessStartsPoolAfterBoundedWaitWhenFirstRefillNeverFinishes(t *testing.T) {
	a := newFixtureApp(t)
	a.firstRefillTimeout = 100 * time.Millisecond
	a.refillLoopFn = func(ctx context.Context, root string) { <-ctx.Done() } // never marks the first Refill done
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // ends the stand-in loop; the run itself must NOT depend on this

	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); a.runHeadless(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runHeadless hung although the first Refill never finished; the wait is not bounded")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("runHeadless returned after %v, before the %v first-refill timeout elapsed", elapsed, a.firstRefillTimeout)
	}
}

func TestWaitForFirstRefillReportsWhyItStoppedWaiting(t *testing.T) {
	t.Run("finished", func(t *testing.T) {
		a := newFixtureApp(t)
		a.markFirstRefillDone()
		a.markFirstRefillDone() // idempotent: a second signal must not panic on a closed channel
		if !a.waitForFirstRefill(context.Background(), time.Minute) {
			t.Fatal("want true once the first Refill is done")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		a := newFixtureApp(t)
		if a.waitForFirstRefill(context.Background(), 10*time.Millisecond) {
			t.Fatal("want false when the timeout elapses first")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		a := newFixtureApp(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if a.waitForFirstRefill(ctx, time.Minute) {
			t.Fatal("want false when ctx is cancelled first")
		}
	})
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

type recordingBd struct {
	mu      sync.Mutex
	titles  []string
	labels  [][]string
	bodies  []string
	repoDir []string
}

func (r *recordingBd) client(repoRoot string) beads.BdClient {
	return &recordingClient{r: r, repoRoot: repoRoot}
}

type recordingClient struct {
	r        *recordingBd
	repoRoot string
}

func (c *recordingClient) Create(title, body string, labels []string, priority int) (string, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.titles = append(c.r.titles, title)
	c.r.bodies = append(c.r.bodies, body)
	c.r.labels = append(c.r.labels, labels)
	c.r.repoDir = append(c.r.repoDir, c.repoRoot)
	return "pg2-recorded", nil
}

// TestSweepFilesOneTriageBeadWhenTheLastFileOfAPackageIsRecorded drives the
// whole wiring (discovery, queue, pool, ledger, filer) over a two-file package
// with a stub analyser that writes real-shaped reports. The bd client is a
// recorder here: the contract with the real bd is proved in
// internal/beads/realbd_test.go.
func TestSweepFilesOneTriageBeadWhenTheLastFileOfAPackageIsRecorded(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil { // repository-root marker only; the filer never runs git
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("Every bead filed against this repo MUST carry the repo label `base`.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(repo, "mod", "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(repo, "mod", "go.mod"): "module fixture\n\ngo 1.22\n",
		filepath.Join(pkg, "a.go"):           "package pkg\n",
		filepath.Join(pkg, "b.go"):           "package pkg\n",
		filepath.Join(pkg, "a_test.go"):      "package pkg\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	reports := t.TempDir()
	rec := &recordingBd{}
	app, err := newApp(appOptions{
		Root:        repo,
		Concurrency: 2,
		NewBdClient: rec.client,
		StubRunFunc: func(file string) (string, string, error) {
			rp := filepath.Join(reports, filepath.Base(file)+".json")
			body := `{"survivors":[{"file":"","line":3,"type":"conditional","description":"x -> y"}],"statistics":{"killed":1,"survived":1}}`
			return "done", rp, os.WriteFile(rp, []byte(body), 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app.runHeadless(ctx)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.titles) != 1 {
		t.Fatalf("want exactly one triage bead for the package, got %d: %v", len(rec.titles), rec.titles)
	}
	if rec.titles[0] != "go-test-gaps triage: pkg" {
		t.Errorf("title = %q", rec.titles[0])
	}
	if got := strings.Join(rec.labels[0], ","); got != "go-test-gaps,base" {
		t.Errorf("labels = %s", got)
	}
	if rec.repoDir[0] != repo {
		t.Errorf("filed in tracker %q, want the repository root %q", rec.repoDir[0], repo)
	}
	for _, want := range []string{"`pkg/a.go:3`", "`pkg/b.go:3`", beads.HowToWorkThisBead} {
		if !strings.Contains(rec.bodies[0], want) {
			t.Errorf("body lacks %q\n%s", want, rec.bodies[0])
		}
	}
}
