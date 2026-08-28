package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/metrics"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/obslog"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/queue"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/tui"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/worker"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/phillipgreenii/x/gitclient"
)

var version = "dev"

// metricsAddr binds the /metrics endpoint to loopback only, matching every
// other metricsTargets registration in this workspace ("Port of the app's
// Prometheus /metrics endpoint (on 127.0.0.1)") -- a bare ":9464" binds all
// interfaces, exposing operational data (queue depth, error rates) to
// anything else on the local network.
const metricsAddr = "127.0.0.1:9464"

type appOptions struct {
	Root        string
	Concurrency int
	StubRunFunc worker.RunFunc // non-nil only in tests; production wires runViaPgGoMutate
}

type app struct {
	q             *queue.Queue
	ledger        *ledger.Ledger
	pool          *worker.Pool
	metrics       *metrics.Registry
	root          string
	concurrency   int
	lowWatermark  int
	highWatermark int
}

// xdgConfigHome and xdgStateHome apply the same "${VAR:-default}" fallback
// pg-go-mutate's bash side and design §11 both require. Both FAIL rather
// than fall back to a bare os.Getenv("HOME") when HOME is also unset: an
// empty HOME would silently resolve to a CWD-relative path ("./.config",
// "./.local/state"), so wherever the binary happens to be invoked from
// would decide where its config is read from and its state/logs are
// written -- a caller-controlled relative path, not a hardened absolute
// one.
func xdgConfigHome() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("pg-go-mutate-tui: neither XDG_CONFIG_HOME nor HOME is set")
	}
	return filepath.Join(home, ".config"), nil
}

func xdgStateHome() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v, nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("pg-go-mutate-tui: neither XDG_STATE_HOME nor HOME is set")
	}
	return filepath.Join(home, ".local", "state"), nil
}

func newApp(opts appOptions) (*app, error) {
	logger := obslog.New()
	configHome, err := xdgConfigHome()
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(configHome, "pg-go-mutate-tui", "config.json")
	staticCfg, err := config.LoadStatic(cfgPath, logger)
	if err != nil {
		return nil, err
	}
	concurrency := staticCfg.Concurrency
	if opts.Concurrency > 0 {
		concurrency = opts.Concurrency
	}

	stateHome, err := xdgStateHome()
	if err != nil {
		return nil, err
	}
	statePath := filepath.Join(stateHome, "pg-go-mutate-tui", "ledger.jsonl")
	l := ledger.New(statePath)
	q := queue.NewQueue(staticCfg.LowWatermark, staticCfg.HighWatermark, staticCfg.RefillRetry)
	m := metrics.New()

	run := opts.StubRunFunc
	if run == nil {
		run = runViaPgGoMutate(m)
	}
	pool := worker.New(q, l, run)

	return &app{
		q:             q,
		ledger:        l,
		pool:          pool,
		metrics:       m,
		root:          opts.Root,
		concurrency:   concurrency,
		lowWatermark:  staticCfg.LowWatermark,
		highWatermark: staticCfg.HighWatermark,
	}, nil
}

// runViaPgGoMutate shells out to the extended pg-go-mutate for a single
// file, recording BOTH metric families (design §12): command-execution
// success/error is the process-level outcome; run-result is the diagnostic
// status pg-go-mutate reports once it has run at all.
func runViaPgGoMutate(m *metrics.Registry) worker.RunFunc {
	return func(file string) (string, string, error) {
		start := time.Now()
		cmd := exec.Command("pg-go-mutate", "--json", file)
		out, err := cmd.Output()
		m.ObserveRunDuration(time.Since(start))
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				m.RecordCommandExecution("error") // binary missing, couldn't even start
				return "failed", "", err
			}
		}
		m.RecordCommandExecution("succeeded") // it ran and returned SOME exit code
		exitCode := cmd.ProcessState.ExitCode()
		if exitCode == 13 {
			// Environment precondition failed (go/gomu missing or version-
			// mismatched) -- this would fail EVERY remaining file
			// identically, so it aborts the whole pool (design §6.2) rather
			// than being recorded as an ordinary per-file result.
			m.RecordRunResult("failed")
			return "", "", worker.ErrFatal
		}
		status := classifyExitCode(exitCode)
		m.RecordRunResult(status)
		reportPath := file + ".report.json"
		if writeErr := os.WriteFile(reportPath, out, 0o644); writeErr != nil {
			return status, "", writeErr
		}
		return status, reportPath, nil
	}
}

// classifyExitCode maps pg-go-mutate's exit-code contract (design §5.1,
// unchanged from ADR-0026's original 0/1/2/10-14 allocation) to the
// ledger's status vocabulary (design §6.2). Exit 13 is handled BEFORE this
// function is called (see above) -- it never reaches here.
func classifyExitCode(code int) string {
	switch code {
	case 0:
		return "done"
	case 10:
		return "no-tests"
	case 11:
		return "not-enumerable"
	case 12:
		return "unhealthy"
	case 14:
		return "vanished"
	default:
		return "failed"
	}
}

// refillLoop is the dedicated goroutine design §7 requires, isolated from
// both the UI loop and the per-run worker goroutines. It ranks packages by
// git recency (via x/gitclient's HistoryReader role) and calls Queue.Refill
// whenever the queue is below the low mark, backing off per
// Queue.RetryBackoffElapsed when a refill finds nothing.
//
// The gitclient.Client is anchored at root once, here, rather than
// per-lookup: per gitclient's own doc comment it is safe for concurrent use
// and holds no per-call state, and root is fixed for refillLoop's lifetime.
// If root is not (or is not yet) a git repository -- e.g. a bare scan
// directory in a unit test -- New fails once and gitClientErr is recorded;
// every recency lookup below then degrades to time.Time{} (the same "give
// up and rank it as unknown" behavior the old code produced for ANY git
// failure), rather than the whole loop failing.
func (a *app) refillLoop(ctx context.Context, root string) {
	gitClient, gitClientErr := gitclient.New(ctx, root)
	recency := func(pkgPath string) time.Time {
		if gitClientErr != nil {
			return time.Time{}
		}
		commits, err := gitClient.Commits(ctx, gitclient.LogOptions{Limit: 1, Paths: []string{pkgPath}})
		if err != nil || len(commits) == 0 {
			return time.Time{}
		}
		return commits[0].Committer.When // %ct recency queue.RecencyLookup ranks by
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !a.q.NeedsRefill() {
			continue
		}
		if !a.q.RetryBackoffElapsed() {
			continue
		}
		projects, err := discover.DiscoverProjects([]string{root})
		if err != nil {
			continue
		}
		var candidates []discover.Package
		for _, p := range projects {
			pkgs, err := discover.DiscoverPackages(p)
			if err != nil {
				continue
			}
			candidates = append(candidates, pkgs...)
		}
		added, err := a.q.Refill(candidates, recency, a.ledger)
		if err != nil || added == 0 {
			a.q.RecordEmptyRefill()
		}
	}
}

// runHeadless drives discovery + refill + the worker pool with no TUI
// attached -- used by the integration test, and by a future --no-tui/batch
// mode if one is ever wanted; the real interactive path (run) additionally
// starts the bubbletea Program.
func (a *app) runHeadless(ctx context.Context) {
	go a.refillLoop(ctx, a.root)
	// Give the refill loop one tick to populate the queue before the pool
	// starts polling it, matching the ticker interval above.
	time.Sleep(1100 * time.Millisecond)
	a.pool.Run(ctx, a.concurrency)
	if err := a.pool.FatalErr(); err != nil {
		obslog.New().Error("pool aborted on a fatal error", "error", err)
	}
}

// newTUIModel wraps the primary screen's Model (internal/tui) with an
// initial snapshot of a's live state. It lives here in package main, not
// internal/tui, because it takes *app -- an internal/tui package cannot
// import package main without an import cycle.
func newTUIModel(a *app) tea.Model {
	return tui.NewModel(tui.PrimaryState{
		Concurrency:    a.concurrency,
		ConcurrencyMax: a.concurrency,
		QueueDepth:     a.q.Depth(),
		LowWatermark:   a.lowWatermark,
		HighWatermark:  a.highWatermark,
	})
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("pg-go-mutate-tui", flag.ContinueOnError)
	root := fs.String("root", "", "root directory to scan (required)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: --root is required")
		return 2
	}

	a, err := newApp(appOptions{Root: *root})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go a.refillLoop(ctx, *root)
	go func() {
		a.pool.Run(ctx, a.concurrency)
		if err := a.pool.FatalErr(); err != nil {
			fmt.Fprintln(os.Stderr, "pg-go-mutate-tui: aborted:", err)
			stop() // cancel ctx so the TUI program and refillLoop also unwind
		}
	}()
	go func() {
		if err := http.ListenAndServe(metricsAddr, a.metrics.Handler()); err != nil {
			obslog.New().Error("metrics server failed", "error", err)
		}
	}()

	// design §7: quitting the TUI is the hard-stop path -- cancelling ctx
	// (via signal.NotifyContext above, or the 'q' key inside the bubbletea
	// program) stops new dispatch; in-flight pg-go-mutate subprocesses are
	// allowed to finish rather than being killed mid-write.
	p := tea.NewProgram(newTUIModel(a))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pg-go-mutate-tui:", err)
		return 1
	}
	return 0
}
