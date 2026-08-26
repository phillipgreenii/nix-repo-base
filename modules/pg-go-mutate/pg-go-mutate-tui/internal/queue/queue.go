// Package queue implements pg-go-mutate-tui's dynamic work queue: a
// watermark-driven, recency-ranked, real-package-hash-backed pending list of
// files awaiting a mutation run, plus the low-mark retry backoff timer.
package queue

import (
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash"
)

type entry struct {
	file, pkgHash string
}

// RecencyLookup returns the last-changed time for the package at pkgPath.
// The queue package itself has no git dependency -- the caller supplies
// this (Task 16's integration wiring provides the production implementation
// via `git log -1 --format=%ct -- <pkgPath>`) -- which keeps this package
// independently testable.
type RecencyLookup func(pkgPath string) time.Time

// Queue is accessed concurrently by design: Task 16's refillLoop goroutine
// calls Refill while every worker goroutine (Task 9) calls Pop at the same
// time. Every method MUST take mu -- there is no other synchronization
// between the refill goroutine and the worker pool.
type Queue struct {
	mu              sync.Mutex
	low, high       int
	retryBackoff    time.Duration
	pending         []entry
	lastEmptyRefill time.Time
	projectFilter   map[string]bool
}

// NewQueue returns a Queue whose NeedsRefill reports true whenever its
// depth drops below low, and whose RetryBackoffElapsed waits retryBackoff
// after a RecordEmptyRefill call. high is retained for callers/future use
// (e.g. capping how many entries a single Refill may add) but is not
// enforced by any method here -- no test in this task exercises a
// high-mark cap.
func NewQueue(low, high int, retryBackoff time.Duration) *Queue {
	return &Queue{low: low, high: high, retryBackoff: retryBackoff}
}

// Depth returns the number of files currently pending.
func (q *Queue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// NeedsRefill reports whether the queue's depth has dropped below its low
// watermark.
func (q *Queue) NeedsRefill() bool { return q.Depth() < q.low }

// Refill computes each candidate package's REAL current content hash via
// pkghash.Compute -- this is the only place that hash is captured for
// queued work, and it is captured here, at refill/dispatch-preparation
// time, not recomputed later (design §6.3). pkghash.Compute and l.NeedsRun
// are called OUTSIDE the lock (they only touch the filesystem/ledger, not
// q's own fields), and only the final append to q.pending is guarded --
// this keeps the lock held for a bounded, short critical section rather
// than for the whole (potentially slow, many-package) refill pass.
//
// Packages are ranked by recency descending before their files are
// appended, so more-recently-changed packages surface earlier in Pop
// order. If pkghash.Compute fails for any candidate (e.g. its directory
// vanished mid-scan), Refill returns immediately with the count of files
// added so far and the error -- it does not silently treat the failure as
// "no files need a run".
func (q *Queue) Refill(candidates []discover.Package, recency RecencyLookup, l *ledger.Ledger) (int, error) {
	sorted := make([]discover.Package, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool {
		return recency(sorted[i].PkgPath).After(recency(sorted[j].PkgPath))
	})
	q.mu.Lock()
	filter := q.projectFilter
	q.mu.Unlock()

	added := 0
	for _, pkg := range sorted {
		if len(filter) > 0 && !filter[pkg.ProjectKey] {
			continue
		}
		hash, err := pkghash.Compute(pkg.AbsPath)
		if err != nil {
			return added, err
		}
		for _, f := range pkg.Files {
			if l.NeedsRun(f, hash) {
				q.mu.Lock()
				q.pending = append(q.pending, entry{file: f, pkgHash: hash})
				q.mu.Unlock()
				added++
			}
		}
	}
	q.mu.Lock()
	if added > 0 {
		q.lastEmptyRefill = time.Time{}
	}
	q.mu.Unlock()
	return added, nil
}

// Pop removes and returns the oldest pending entry (FIFO). pkgHash is the
// real digest captured at Refill time; ok is false when the queue is empty.
func (q *Queue) Pop() (file, pkgHash string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return "", "", false
	}
	e := q.pending[0]
	q.pending = q.pending[1:]
	return e.file, e.pkgHash, true
}

// SetProjectFilter restricts future Refill calls to only the given
// projects, keyed by discover.Package.ProjectKey -- for the primary
// screen's per-project include/exclude checkboxes (debounced project-filter
// wiring, Task 16). A nil or empty map means no restriction: every project
// is eligible, matching Refill's behavior before this method existed.
func (q *Queue) SetProjectFilter(included map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.projectFilter = included
}

// RecordEmptyRefill marks now as the start of the retry backoff window,
// for use after a Refill call that added nothing.
func (q *Queue) RecordEmptyRefill() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.lastEmptyRefill = time.Now()
}

// RetryBackoffElapsed reports whether retryBackoff has elapsed since the
// last RecordEmptyRefill call. It reports true if RecordEmptyRefill has
// never been called (or was cleared by a non-empty Refill).
func (q *Queue) RetryBackoffElapsed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lastEmptyRefill.IsZero() {
		return true
	}
	return time.Since(q.lastEmptyRefill) >= q.retryBackoff
}
