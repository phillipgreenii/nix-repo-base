package exec

import (
	"context"
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
	inner   Runner
	jobs    chan func()
	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
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
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func (p *WorkerPool) worker() {
	defer p.wg.Done()
	for job := range p.jobs {
		job()
	}
}

// Submit schedules `fn` for execution on a worker goroutine. Submit blocks
// when all workers are busy; this gives natural backpressure.
func (p *WorkerPool) Submit(fn func()) {
	p.jobs <- fn
}

// Close stops accepting new jobs and waits for the in-flight ones to finish.
// Safe to call multiple times.
func (p *WorkerPool) Close() {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return
	}
	p.closed = true
	close(p.jobs)
	p.closeMu.Unlock()
	p.wg.Wait()
}

// Run forwards to the inner Runner. Implements Runner.
func (p *WorkerPool) Run(ctx context.Context, name string, args []string, opts RunOptions) (Result, error) {
	return p.inner.Run(ctx, name, args, opts)
}

// ensure WorkerPool satisfies Runner at compile time
var _ Runner = (*WorkerPool)(nil)
