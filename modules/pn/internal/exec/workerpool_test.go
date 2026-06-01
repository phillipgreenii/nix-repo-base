package exec

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerPool_RunsJobsInParallelUpToLimit(t *testing.T) {
	const workers = 3
	const jobs = 9
	pool := NewWorkerPool(NewFakeRunner(), workers)

	var inFlight, peak int64
	track := func() {
		cur := atomic.AddInt64(&inFlight, 1)
		// Race-clean: only swap if cur > current peak.
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old {
				break
			}
			if atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
	}
	release := func() { atomic.AddInt64(&inFlight, -1) }

	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		pool.Submit(func() {
			defer wg.Done()
			track()
			time.Sleep(50 * time.Millisecond)
			release()
		})
	}
	wg.Wait()
	pool.Close()

	finalPeak := atomic.LoadInt64(&peak)
	if finalPeak > int64(workers) {
		t.Errorf("peak in-flight = %d; pool size %d; should never exceed", finalPeak, workers)
	}
	if finalPeak < int64(workers) {
		t.Logf("peak in-flight = %d (workers = %d); expected = workers when jobs ≫ workers", finalPeak, workers)
	}
}

func TestWorkerPool_ClosureCanCaptureErrors(t *testing.T) {
	pool := NewWorkerPool(NewFakeRunner(), 2)
	var collected []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 3; i++ {
		i := i
		pool.Submit(func() {
			defer wg.Done()
			if i == 1 {
				mu.Lock()
				collected = append(collected, errors.New("job 1 failed"))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	pool.Close()
	if len(collected) != 1 || collected[0].Error() != "job 1 failed" {
		t.Errorf("collected errors: %v", collected)
	}
}

func TestWorkerPool_CloseWaitsForInflight(t *testing.T) {
	pool := NewWorkerPool(NewFakeRunner(), 2)
	var jobFinished int64
	pool.Submit(func() {
		time.Sleep(100 * time.Millisecond)
		atomic.StoreInt64(&jobFinished, 1)
	})
	pool.Close() // must block until the slow job finishes
	if atomic.LoadInt64(&jobFinished) != 1 {
		t.Errorf("Close returned before in-flight job finished")
	}
}

func TestWorkerPool_RunMethodDispatchesToInnerRunner(t *testing.T) {
	inner := NewFakeRunner()
	inner.AddResponse("echo", []string{"hi"}, Result{Stdout: []byte("hi\n")}, nil)
	pool := NewWorkerPool(inner, 2)
	res, err := pool.Run(context.Background(), "echo", []string{"hi"}, RunOptions{})
	pool.Close()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hi\n" {
		t.Errorf("Run stdout: %q", res.Stdout)
	}
}
