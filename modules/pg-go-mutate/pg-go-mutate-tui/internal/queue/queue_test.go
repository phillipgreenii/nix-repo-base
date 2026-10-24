package queue

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/discover"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/ledger"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-go-mutate/pg-go-mutate-tui/internal/pkghash"
)

func writePkg(t *testing.T, root, name, content string) discover.Package {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(content), 0o644)
	return discover.Package{PkgPath: name, AbsPath: dir, Files: []string{filepath.Join(dir, "a.go")}}
}

func TestNeedsRefillBelowLowMark(t *testing.T) {
	q := NewQueue(2, 10, time.Minute)
	if q.NeedsRefill() != true { // starts empty, below low mark of 2
		t.Fatal("empty queue must need refill")
	}
}

func TestRefillStampsTheRealContentHashNotThePackagePath(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	_, hash, ok := q.Pop()
	if !ok {
		t.Fatal("expected one queued file")
	}
	if hash == "pkg" || len(hash) != 64 { // sha256 hex digest length
		t.Fatalf("expected a real sha256 digest, got %q (looks like the path was stored instead)", hash)
	}
}

func TestRefillReQueuesAFileAfterItsPackageIsEdited(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	file, hash, _ := q.Pop()
	l.Append(ledger.Record{FilePath: file, PackageHash: hash, Status: "done"})
	q.Finish(file) // the worker's post-append step

	// No edit yet: a second refill must NOT re-add the file.
	added, _ := q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	if added != 0 {
		t.Fatalf("expected 0 files added for an unchanged package, got %d", added)
	}

	// Edit the package's only file -- its content hash changes.
	os.WriteFile(filepath.Join(pkg.AbsPath, "a.go"), []byte("package pkg\nfunc A() { /* changed */ }\n"), 0o644)
	added, err := q.Refill([]discover.Package{pkg}, func(string) time.Time { return time.Now() }, l)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("expected the edited package's file to be re-queued, got %d added", added)
	}
}

func TestRefillOrdersPackagesByRecencyDescending(t *testing.T) {
	root := t.TempDir()
	oldPkg := writePkg(t, root, "old", "package old\n")
	newPkg := writePkg(t, root, "new", "package new\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(1, 10, time.Minute)

	recency := map[string]time.Time{"old": time.Now().Add(-48 * time.Hour), "new": time.Now()}
	q.Refill([]discover.Package{oldPkg, newPkg}, func(p string) time.Time { return recency[p] }, l)

	first, _, _ := q.Pop()
	if filepath.Base(filepath.Dir(first)) != "new" {
		t.Fatalf("expected the more-recently-changed package's file first, got %s", first)
	}
}

func TestRetryBackoffElapsed(t *testing.T) {
	q := NewQueue(1, 10, 10*time.Millisecond)
	q.RecordEmptyRefill()
	if q.RetryBackoffElapsed() {
		t.Fatal("must not be elapsed immediately")
	}
	time.Sleep(20 * time.Millisecond)
	if !q.RetryBackoffElapsed() {
		t.Fatal("must be elapsed after the backoff duration")
	}
}

func TestSetProjectFilterExcludesNonIncludedProjectsFromRefill(t *testing.T) {
	root := t.TempDir()
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(0, 10, time.Minute)
	q.SetProjectFilter(map[string]bool{"keep": true})

	kept := writePkg(t, root, "keeppkg", "package keeppkg\nfunc A() {}\n")
	kept.ProjectKey = "keep"
	skipped := writePkg(t, root, "skippkg", "package skippkg\nfunc B() {}\n")
	skipped.ProjectKey = "skip"

	added, err := q.Refill([]discover.Package{kept, skipped}, func(string) time.Time { return time.Now() }, l)
	if err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if added != 1 {
		t.Fatalf("expected 1 file added (the included project only), got %d", added)
	}
	file, _, ok := q.Pop()
	if !ok {
		t.Fatal("expected one queued file")
	}
	if !strings.Contains(file, "keeppkg") {
		t.Fatalf("expected the queued file to be from the included project, got %q", file)
	}
	if _, _, ok := q.Pop(); ok {
		t.Fatal("expected the excluded project's file to never be queued")
	}
}

func TestConcurrentRefillAndPopDoNotRace(t *testing.T) {
	root := t.TempDir()
	l := ledger.New(root + "/ledger.jsonl")
	q := NewQueue(0, 1000, time.Minute)
	var pkgs []discover.Package
	for i := 0; i < 20; i++ {
		pkgs = append(pkgs, writePkg(t, root, "pkg"+string(rune('a'+i)), "package p\n"))
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			q.Refill(pkgs, func(string) time.Time { return time.Now() }, l)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			q.Pop()
		}
	}()
	wg.Wait()
}

func twoFilePkg(t *testing.T, root string) discover.Package {
	t.Helper()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	second := filepath.Join(pkg.AbsPath, "b.go")
	if err := os.WriteFile(second, []byte("package pkg\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg.Files = append(pkg.Files, second)
	return pkg
}

func now(string) time.Time { return time.Now() }

func TestRefillTwiceWithoutPopQueuesEachFileOnce(t *testing.T) {
	root := t.TempDir()
	pkg := twoFilePkg(t, root)
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(10, 100, time.Minute)

	first, err := q.Refill([]discover.Package{pkg}, now, l)
	if err != nil || first != 2 {
		t.Fatalf("first Refill = %d, %v; want 2, nil", first, err)
	}
	second, err := q.Refill([]discover.Package{pkg}, now, l)
	if err != nil || second != 0 {
		t.Fatalf("second Refill = %d, %v; want 0 (already pending), nil", second, err)
	}
	seen := map[string]int{}
	for {
		f, _, ok := q.Pop()
		if !ok {
			break
		}
		seen[f]++
	}
	if len(seen) != 2 {
		t.Fatalf("expected both files popped, got %v", seen)
	}
	for f, n := range seen {
		if n != 1 {
			t.Fatalf("file %s popped %d times, want exactly once", f, n)
		}
	}
}

func TestRefillDoesNotRequeueAPoppedButUnfinishedFile(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(10, 100, time.Minute)

	q.Refill([]discover.Package{pkg}, now, l)
	if _, _, ok := q.Pop(); !ok {
		t.Fatal("expected one queued file")
	}
	// In flight: popped, no ledger record, not finished.
	added, err := q.Refill([]discover.Package{pkg}, now, l)
	if err != nil || added != 0 {
		t.Fatalf("Refill of an in-flight file = %d, %v; want 0, nil", added, err)
	}
	if q.Depth() != 0 {
		t.Fatalf("depth = %d, want 0", q.Depth())
	}
}

func TestRefillDoesNotRequeueAFinishedFileRecordedAtTheSameHash(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(10, 100, time.Minute)

	q.Refill([]discover.Package{pkg}, now, l)
	file, hash, _ := q.Pop()
	l.Append(ledger.Record{FilePath: file, PackageHash: hash, Status: "done"})
	q.Finish(file)

	added, err := q.Refill([]discover.Package{pkg}, now, l)
	if err != nil || added != 0 {
		t.Fatalf("Refill after finish = %d, %v; want 0, nil", added, err)
	}
}

func TestRefillRequeuesAFinishedFileWithNoLedgerRecord(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(10, 100, time.Minute)

	q.Refill([]discover.Package{pkg}, now, l)
	file, _, _ := q.Pop()
	q.Finish(file) // a run that recorded nothing: the ledger still says "needs run"

	added, _ := q.Refill([]discover.Package{pkg}, now, l)
	if added != 1 {
		t.Fatalf("Refill after an unrecorded finish = %d, want 1", added)
	}
}

func TestHashChangeWhileInFlightSelfHealsAfterFinish(t *testing.T) {
	root := t.TempDir()
	pkg := writePkg(t, root, "pkg", "package pkg\nfunc A() {}\n")
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	q := NewQueue(10, 100, time.Minute)

	q.Refill([]discover.Package{pkg}, now, l)
	file, oldHash, _ := q.Pop()

	// The package is edited while the file is still running.
	os.WriteFile(filepath.Join(pkg.AbsPath, "a.go"), []byte("package pkg\nfunc A() { /* edited */ }\n"), 0o644)
	added, _ := q.Refill([]discover.Package{pkg}, now, l)
	if added != 0 {
		t.Fatalf("Refill while the old entry is outstanding = %d, want 0", added)
	}

	l.Append(ledger.Record{FilePath: file, PackageHash: oldHash, Status: "done"})
	q.Finish(file)

	added, _ = q.Refill([]discover.Package{pkg}, now, l)
	if added != 1 {
		t.Fatalf("Refill after finish with a changed hash = %d, want 1 (self-heal)", added)
	}
	_, newHash, ok := q.Pop()
	if !ok || newHash == oldHash {
		t.Fatalf("expected re-queue at a new hash, got ok=%v hash=%q (old %q)", ok, newHash, oldHash)
	}
}

func TestRefillQueuesOnlyTheFilesTheLedgerLacks(t *testing.T) {
	root := t.TempDir()
	pkg := twoFilePkg(t, root)
	l := ledger.New(filepath.Join(root, "ledger.jsonl"))
	hash, err := pkghash.Compute(pkg.AbsPath)
	if err != nil {
		t.Fatal(err)
	}
	l.Append(ledger.Record{FilePath: pkg.Files[0], PackageHash: hash, Status: "done"})
	q := NewQueue(10, 100, time.Minute)

	added, err := q.Refill([]discover.Package{pkg}, now, l)
	if err != nil || added != 1 {
		t.Fatalf("Refill = %d, %v; want 1, nil", added, err)
	}
	if f, _, _ := q.Pop(); f != pkg.Files[1] {
		t.Fatalf("queued %s, want %s", f, pkg.Files[1])
	}
}
