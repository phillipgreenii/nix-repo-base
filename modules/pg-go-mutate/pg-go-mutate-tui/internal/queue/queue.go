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
// this (main.go's refillLoop provides the production implementation via
// x/gitclient's HistoryReader role, i.e. Commits' %ct Committer.When) --
// which keeps this package independently testable.
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
	outstanding     map[string]struct{} // files pending OR in flight, until Finish
	finishSeq       uint64              // incremented by every Finish
	finishedAt      map[string]uint64   // file -> finishSeq value at its last Finish
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
// time, not recomputed later (design §6.3). The ledger is replayed once per
// call, and pkghash.Compute and that replay run OUTSIDE the lock (they only
// touch the filesystem/ledger, not q's own fields); only the per-file
// outstanding check plus append is guarded -- this keeps the lock held for
// a bounded, short critical section rather than for the whole (potentially
// slow, many-package) refill pass.
//
// A file already pending or in flight (appended by an earlier Refill and not
// yet released by Finish) is skipped, so a file is dispatched at most once
// per entry however many refill ticks happen while it is queued or running.
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
	// Note the finish sequence BEFORE the ledger is replayed: a file whose
	// Finish lands after this point may have its ledger record missing from
	// the snapshot below (the worker appends, then finishes, while Replay is
	// reading), so it must not be judged against that snapshot this round.
	snapshotSeq := q.finishSeq
	q.mu.Unlock()

	// Replay the ledger ONCE per call and look files up in the map, rather
	// than a full ledger read per candidate file. A Replay error is treated
	// as "nothing recorded" (every file needs a run), the same safe direction
	// ledger.NeedsRun takes.
	records, err := l.Replay()
	if err != nil {
		records = nil
	}

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
			if r, ok := records[f]; ok && r.PackageHash == hash {
				continue // already analysed at this hash
			}
			q.mu.Lock()
			// A file whose entry is pending or in flight is never queued
			// again, even if its package hash has since changed: that
			// self-heal (design §6.3) happens on the first Refill after
			// Finish removes the file from the outstanding set.
			_, busy := q.outstanding[f]
			// Finished after the ledger snapshot was taken: the snapshot may
			// predate its record, so leave it to the next Refill.
			stale := q.finishedAt[f] > snapshotSeq
			if !busy && !stale {
				if q.outstanding == nil {
					q.outstanding = make(map[string]struct{})
				}
				q.outstanding[f] = struct{}{}
				q.pending = append(q.pending, entry{file: f, pkgHash: hash})
				added++
			}
			q.mu.Unlock()
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

// Finish tells the queue a worker is done with file: its entry is no longer
// outstanding (pending or in flight), so a later Refill consults the ledger
// for it again. A worker MUST call Finish after its ledger append (or after
// a run that records nothing), never before -- otherwise a Refill in the gap
// would see neither an outstanding entry nor a ledger record and re-queue
// the file.
func (q *Queue) Finish(file string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.outstanding, file)
	q.finishSeq++
	if q.finishedAt == nil {
		q.finishedAt = make(map[string]uint64)
	}
	q.finishedAt[file] = q.finishSeq
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
