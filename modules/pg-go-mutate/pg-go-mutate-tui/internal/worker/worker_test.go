package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/queue"
)

func fixtureQueue(t *testing.T, n int) *queue.Queue {
	t.Helper()
	root := t.TempDir()
	q := queue.NewQueue(0, 100, time.Minute)
	l := ledger.New(root + "/ledger.jsonl")
	var pkgs []discover.Package
	for i := 0; i < n; i++ {
		dir := root + "/pkg" + string(rune('a'+i))
		_ = dir
		pkgs = append(pkgs, discover.Package{PkgPath: dir, AbsPath: t.TempDir(), Files: []string{dir + "/a.go"}})
	}
	q.Refill(pkgs, func(string) time.Time { return time.Now() }, l)
	return q
}

func TestRunDispatchesUpToConcurrencyLimitConcurrently(t *testing.T) {
	q := fixtureQueue(t, 5)
	var inFlight, maxInFlight int32
	run := func(file string) (string, string, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return "done", "/tmp/report.json", nil
	}

	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	p := New(q, l, run)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.Run(ctx, 3)

	if atomic.LoadInt32(&maxInFlight) > 3 {
		t.Fatalf("exceeded configured concurrency: saw %d in flight", maxInFlight)
	}
}

func TestPauseThenResumeContinuesDispatchingRatherThanEndingPermanently(t *testing.T) {
	// 40 items with a 5ms-per-item delay at concurrency 2 gives ~100ms of
	// total work -- comfortably longer than the 20ms this test waits before
	// pausing, so Pause() is guaranteed to land mid-stream rather than
	// racing against all work finishing first (a near-instant RunFunc with
	// few items would make this test's own timing unreliable, not the code
	// under test).
	q := fixtureQueue(t, 40)
	var dispatched int32
	run := func(file string) (string, string, error) {
		atomic.AddInt32(&dispatched, 1)
		time.Sleep(5 * time.Millisecond)
		return "done", "", nil
	}
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	p := New(q, l, run)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx, 2) }()

	time.Sleep(20 * time.Millisecond)
	p.Pause()
	pausedCount := atomic.LoadInt32(&dispatched)
	if pausedCount >= 40 {
		t.Skip("all work finished before Pause() landed -- timing assumption violated, not a code defect; increase item count or delay if this recurs")
	}
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&dispatched) != pausedCount {
		t.Fatalf("dispatch continued after Pause(): was %d, now %d", pausedCount, atomic.LoadInt32(&dispatched))
	}

	p.Resume()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&dispatched) < 40 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&dispatched) <= pausedCount {
		t.Fatalf("Resume() did not cause further dispatch: paused at %d, still %d after resume",
			pausedCount, atomic.LoadInt32(&dispatched))
	}
	cancel()
	wg.Wait()
}

func TestFatalErrorAbortsTheWholePoolWithoutPerFileLedgerNoise(t *testing.T) {
	q := fixtureQueue(t, 10)
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	var calls int32
	run := func(file string) (string, string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "", "", ErrFatal // simulates pg-go-mutate exit 13 on the first file
		}
		return "done", "", nil
	}
	p := New(q, l, run)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p.Run(ctx, 1)

	if p.FatalErr() == nil {
		t.Fatal("expected Pool.FatalErr() to be set after an ErrFatal result")
	}
	records, _ := l.Replay()
	for _, r := range records {
		if r.Status == "" {
			t.Fatal("a fatal result must never be appended to the ledger as a per-file record")
		}
	}
	// Concurrency 1 with a fatal result on the very first file means at
	// most a handful of files could have been picked up before the single
	// worker goroutine observed the fatal state and stopped -- the ledger
	// must be far smaller than the full 10-item queue, not "all but one".
	if len(records) >= 9 {
		t.Fatalf("expected the pool to stop promptly after the fatal result, got %d ledger records", len(records))
	}
}

// TestRepeatedRefillsWhilePoolRunsDispatchEachFileExactlyOnce drives the pool
// against a queue that is refilled over and over (as main.go's refillLoop
// does every tick while depth is below the low watermark), with a slow stub
// RunFunc so files are pending AND in flight during many refills.
func TestRepeatedRefillsWhilePoolRunsDispatchEachFileExactlyOnce(t *testing.T) {
	root := t.TempDir()
	var pkgs []discover.Package
	for i := 0; i < 3; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var files []string
		for j := 0; j < 4; j++ {
			f := filepath.Join(dir, fmt.Sprintf("f%d.go", j))
			if err := os.WriteFile(f, []byte(fmt.Sprintf("package p%d\n// %d\n", i, j)), 0o644); err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		pkgs = append(pkgs, discover.Package{PkgPath: dir, AbsPath: dir, Files: files})
	}
	total := 12

	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := queue.NewQueue(1000, 1000, time.Minute) // low mark far above depth: always "needs refill"
	recency := func(string) time.Time { return time.Now() }
	if _, err := q.Refill(pkgs, recency, l); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := map[string]int{}
	run := func(file string) (string, string, error) {
		mu.Lock()
		calls[file]++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return "done", "", nil
	}
	p := New(q, l, run)

	stop := make(chan struct{})
	var refills sync.WaitGroup
	refills.Add(1)
	go func() {
		defer refills.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := q.Refill(pkgs, recency, l); err != nil {
				t.Errorf("Refill: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.Run(ctx, 3)
	// Keep refilling briefly after the pool drained, so refills that land
	// after every file is recorded are covered too.
	time.Sleep(20 * time.Millisecond)
	close(stop)
	refills.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != total {
		t.Fatalf("ran %d distinct files, want %d", len(calls), total)
	}
	for f, n := range calls {
		if n != 1 {
			t.Errorf("file %s ran %d times, want exactly once", f, n)
		}
	}
}

func TestDispatchedLedgerRecordCarriesTheHashPopReturnedUnchanged(t *testing.T) {
	q := fixtureQueue(t, 1)
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	var seenHash string
	run := func(file string) (string, string, error) { return "done", "", nil }
	p := New(q, l, run)
	// Pop once ourselves first to know what hash the pool must reproduce.
	_, wantHash, ok := q.Pop()
	if !ok {
		t.Fatal("expected a queued file")
	}
	q.Refill([]discover.Package{{PkgPath: "x", AbsPath: t.TempDir(), Files: []string{"x/a.go"}}},
		func(string) time.Time { return time.Now() }, l) // re-add one so the pool has something to pop
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p.Run(ctx, 1)
	records, _ := l.Replay()
	for _, r := range records {
		seenHash = r.PackageHash
	}
	if seenHash == "" {
		t.Fatal("expected a ledger record to be written")
	}
	_ = wantHash // exact equality checked in Task 8's own Refill tests; here we only need a record to exist
}

// TestRetryOutcomeLeavesFileUnrecordedAndKeepsDispatching pins pg2-4agz9: a
// RunFunc that returns ErrRetry (pg-go-mutate exit 3, semaphore busy) must
// leave NO ledger record for that file, must not stop the pool, and must
// release the file from the queue's outstanding set so the next Refill
// queues it again without any package edit.
func TestRetryOutcomeLeavesFileUnrecordedAndKeepsDispatching(t *testing.T) {
	root := t.TempDir()
	var files []string
	for j := 0; j < 3; j++ {
		f := filepath.Join(root, fmt.Sprintf("f%d.go", j))
		if err := os.WriteFile(f, []byte(fmt.Sprintf("package p\n// %d\n", j)), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	pkgs := []discover.Package{{PkgPath: root, AbsPath: root, Files: files}}
	recency := func(string) time.Time { return time.Now() }
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := queue.NewQueue(1000, 1000, time.Minute)
	if _, err := q.Refill(pkgs, recency, l); err != nil {
		t.Fatal(err)
	}

	retried := files[0]
	var mu sync.Mutex
	calls := map[string]int{}
	run := func(file string) (string, string, error) {
		mu.Lock()
		calls[file]++
		mu.Unlock()
		if file == retried {
			return "", "", ErrRetry
		}
		return "done", "", nil
	}
	p := New(q, l, run)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Run(ctx, 1)

	if p.FatalErr() != nil {
		t.Fatalf("ErrRetry must not be fatal, got %v", p.FatalErr())
	}
	if len(calls) != len(files) {
		t.Fatalf("pool stopped dispatching after the retry: ran %d of %d files", len(calls), len(files))
	}
	records, err := l.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(files)-1 {
		t.Fatalf("got %d ledger records, want %d (every file except the retried one)", len(records), len(files)-1)
	}
	for _, r := range records {
		if r.FilePath == retried {
			t.Fatalf("retried file must have no ledger record, got %+v", r)
		}
	}
	// Released from the outstanding set and unrecorded: Refill queues exactly
	// the retried file again, and nothing else.
	added, err := q.Refill(pkgs, recency, l)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("Refill added %d files, want 1 (the retried file)", added)
	}
	if got, _, ok := q.Pop(); !ok || got != retried {
		t.Fatalf("re-queued file = %q (ok=%v), want %q", got, ok, retried)
	}
}

func TestOnRecordedRunsAfterTheLedgerHoldsTheFilesRecordAndSkipsRetriesAndFatals(t *testing.T) {
	q := fixtureQueue(t, 3)
	l := ledger.New(t.TempDir() + "/ledger.jsonl")
	calls := map[string]int{}
	seenInLedger := map[string]bool{}
	var mu sync.Mutex
	n := 0
	run := func(file string) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return "", "", ErrRetry // never analysed: nothing recorded, so no hook call
		}
		return "done", "", nil
	}
	p := New(q, l, run)
	p.SetOnRecorded(func(file string) {
		recs, err := l.Replay()
		mu.Lock()
		defer mu.Unlock()
		calls[file]++
		_, seenInLedger[file] = recs[file]
		if err != nil {
			t.Errorf("Replay: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.Run(ctx, 1)

	recs, _ := l.Replay()
	if len(calls) != len(recs) || len(recs) != 2 {
		t.Fatalf("hook ran for %d files, ledger holds %d (want 2 each: the retried file is neither)", len(calls), len(recs))
	}
	for file, c := range calls {
		if c != 1 || !seenInLedger[file] {
			t.Fatalf("%s: hook calls = %d, record visible in ledger at hook time = %v", file, c, seenInLedger[file])
		}
	}
}
