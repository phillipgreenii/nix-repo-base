// Package worker implements pg-go-mutate-tui's dispatch pool: a fixed number
// of goroutines that pop files from the queue, invoke an injected RunFunc,
// and append the outcome to the ledger, with working pause/resume and a
// whole-pool fatal-abort path for an environment precondition failure that
// would hit every remaining file identically.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/queue"
)

// ErrFatal is the sentinel a RunFunc returns for a failure that would hit
// EVERY remaining file identically -- an environment precondition (pg-go-
// mutate exit 13: go/gomu missing or mismatched). Design §6.2 requires this
// to abort the WHOLE run rather than being recorded per file (thousands of
// identical "failed" records would be noise, not signal, exactly like the
// sweep's own pre-existing fatal-abort handling for the same exit code).
var ErrFatal = errors.New("pg-go-mutate-tui: fatal environment precondition failure")

// RunFunc analyses one file and reports its outcome. Production code
// (Task 16) wires this to exec.Command("pg-go-mutate", "--json", file);
// tests inject a fake, keeping this package free of any subprocess
// dependency in its own tests.
type RunFunc func(file string) (status, reportPath string, err error)

// Pool dispatches queued files to RunFunc across a fixed number of
// goroutines, appending each outcome to the ledger.
type Pool struct {
	q      *queue.Queue
	ledger *ledger.Ledger
	run    RunFunc

	mu     sync.Mutex
	paused bool
	fatal  error
}

// New returns a Pool that pops files from q, invokes run on each, and
// appends outcomes to l.
func New(q *queue.Queue, l *ledger.Ledger, run RunFunc) *Pool {
	return &Pool{q: q, ledger: l, run: run}
}

// Pause halts new dispatch without touching in-flight work or ending any
// worker goroutine.
func (p *Pool) Pause() { p.mu.Lock(); p.paused = true; p.mu.Unlock() }

// Resume resumes dispatch after a Pause.
func (p *Pool) Resume() { p.mu.Lock(); p.paused = false; p.mu.Unlock() }

func (p *Pool) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// FatalErr returns the fatal error that stopped the pool, if any -- callers
// (main.go, Task 16) MUST check this after Run returns and treat a non-nil
// result as an abort, not an ordinary completion.
func (p *Pool) FatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

func (p *Pool) setFatal(err error) {
	p.mu.Lock()
	if p.fatal == nil {
		p.fatal = err
	}
	p.mu.Unlock()
}

func (p *Pool) isFatal() bool { return p.FatalErr() != nil }

// Run spawns concurrency worker goroutines, each looping: check ctx done /
// fatal / paused, pop a file+hash from the queue, invoke RunFunc, on
// ErrFatal set the pool's fatal state and stop (no ledger write), otherwise
// append a ledger record with the hash passed through unchanged. Run blocks
// until every worker goroutine has stopped -- the queue emptied, ctx was
// cancelled, or a sibling worker observed ErrFatal.
func (p *Pool) Run(ctx context.Context, concurrency int) {
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if p.isFatal() {
					return // a sibling worker already hit ErrFatal -- stop dispatching too
				}
				if p.isPaused() {
					// Idle in place -- do NOT return. Returning here would
					// permanently end this goroutine; once every worker did
					// that, Run()'s wg.Wait() below would unblock and a
					// later Resume() would have nothing left to resume.
					time.Sleep(50 * time.Millisecond)
					continue
				}
				file, pkgHash, ok := p.q.Pop() // pkgHash is the digest Refill captured (§6.3)
				if !ok {
					return
				}
				status, reportPath, err := p.run(file)
				if errors.Is(err, ErrFatal) {
					p.setFatal(err) // recorded on the Pool, NOT appended to the ledger as a per-file entry
					return
				}
				p.ledger.Append(ledger.Record{
					FilePath:    file,
					PackageHash: pkgHash,
					Status:      status,
					ReportPath:  reportPath,
				})
			}
		}()
	}
	wg.Wait()
}
