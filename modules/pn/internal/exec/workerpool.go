package exec

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
)

// WorkerPool dispatches Submit jobs across a bounded number of worker
// goroutines. It also implements Runner by forwarding Run calls to its inner
// Runner — this lets pool consumers pass a single value that doubles as both
// the subprocess executor and the concurrency orchestrator.
//
// Submit jobs run concurrently up to the configured worker count. Run calls
// bypass the worker pool (they invoke the inner runner directly), since the
// caller may want a synchronous, prompt subprocess result. Submit is for
// fan-out; Run is for the single-call path.
type WorkerPool struct {
	inner Runner
	jobs  chan func()
	// done is closed exactly once by Close. Workers select on it to stop, and
	// Submit selects on it so a submit-after-Close drops the job instead of
	// panicking on a send to a closed channel.
	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	mu        sync.Mutex
	panics    []any // values recovered from panicking jobs; see Panics.
}

// NewWorkerPool returns a WorkerPool with `workers` background goroutines.
// `workers` must be >= 1; values less than 1 are clamped to 1.
func NewWorkerPool(inner Runner, workers int) *WorkerPool {
	if workers < 1 {
		workers = 1
	}
	p := &WorkerPool{
		inner: inner,
		jobs:  make(chan func()),
		done:  make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *WorkerPool) worker() {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			p.runJob(job)
		case <-p.done:
			return
		}
	}
}

// runJob runs one job, recovering from a panic so a single misbehaving job
// neither crashes the whole process nor tears down its worker goroutine (which
// would silently shrink the pool). The recovered value is recorded (see Panics)
// and surfaced on stderr rather than swallowed.
func (p *WorkerPool) runJob(job func()) {
	defer func() {
		if r := recover(); r != nil {
			p.mu.Lock()
			p.panics = append(p.panics, r)
			p.mu.Unlock()
			fmt.Fprintf(os.Stderr, "pn: worker recovered from panic: %v\n%s", r, debug.Stack())
		}
	}()
	job()
}

// Submit schedules `fn` for execution on a worker goroutine. Submit blocks
// when all workers are busy; this gives natural backpressure. A submit after
// Close is a no-op (the job is dropped) rather than a panic, so a late Submit
// racing a shutdown cannot crash the process.
func (p *WorkerPool) Submit(fn func()) {
	select {
	case p.jobs <- fn:
	case <-p.done:
		// Pool is closed; drop the job.
	}
}

// Close stops accepting new jobs and waits for the in-flight ones to finish.
// Safe to call multiple times — only the first call signals shutdown; every
// call blocks on Wait until all workers have drained.
//
// Close waits unconditionally: a job that never returns blocks Close forever.
// Jobs are expected to honor the context passed to their subprocess calls, so
// cancelling that context (e.g. on SIGINT) is how a caller unblocks a stuck
// pool; Close does not itself cancel running jobs.
func (p *WorkerPool) Close() {
	p.closeOnce.Do(func() { close(p.done) })
	p.wg.Wait()
}

// Panics returns a copy of the values recovered from any panicking jobs
// (nil when no job panicked). Callers that fan out via Submit can inspect this
// after their own WaitGroup drains to detect a job that panicked.
func (p *WorkerPool) Panics() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.panics) == 0 {
		return nil
	}
	return append([]any(nil), p.panics...)
}

// Run forwards to the inner Runner. Implements Runner.
func (p *WorkerPool) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	return p.inner.Run(ctx, name, args, opts)
}

// ensure WorkerPool satisfies Runner at compile time
var _ Runner = (*WorkerPool)(nil)
