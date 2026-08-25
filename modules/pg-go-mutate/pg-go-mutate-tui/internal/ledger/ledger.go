// Package ledger implements pg-go-mutate-tui's append-only, JSON-lines
// work-tracking log: what has already been analysed, at what package hash,
// and to what result. It is a work-avoidance cache, not a source of truth
// that must survive a crash byte for byte -- losing an unflushed record
// only costs one redundant (idempotent) re-analysis on the next run, never
// an incorrect one. Replay is built around that: it tolerates a truncated
// trailing line, because pg-go-mutate-tui's worker pool appends after every
// analysed file, so a hard kill mid-append is an expected shape of input,
// not a corruption signal.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one ledger entry: the outcome of analysing FilePath at
// PackageHash.
type Record struct {
	FilePath    string    `json:"file_path"`
	PackageHash string    `json:"package_hash"`
	Status      string    `json:"status"`
	ReportPath  string    `json:"report_path"`
	RootGitHash string    `json:"root_git_hash"`
	Timestamp   time.Time `json:"timestamp"`
}

// Ledger is an append-only JSON-lines log backed by the file at path.
type Ledger struct {
	path string

	// mu serializes this process's own Append calls. pg-go-mutate-tui's
	// worker pool appends from multiple goroutines concurrently; each
	// Append writes one already-fully-marshaled line in a single
	// os.File.Write, which is already atomic against a file opened
	// O_APPEND, but the mutex removes any dependence on that per-syscall
	// guarantee and keeps this process's concurrent appends strictly
	// ordered rather than merely non-interleaved.
	mu sync.Mutex
}

// New returns a Ledger backed by the JSON-lines file at path. The file (and
// any missing parent directories) are created lazily, on the first Append;
// New itself performs no I/O.
func New(path string) *Ledger {
	return &Ledger{path: path}
}

// maxLineSize bounds how long a single ledger line may be before Replay
// gives up on the rest of the file. bufio.Scanner's default
// (bufio.MaxScanTokenSize, 64KiB) is already generous for these small,
// fixed-shape records, but a scan that hits that ceiling aborts ALL
// remaining lines, not just the offending one -- so an unusually long but
// legitimate ReportPath would silently discard every record after it. This
// larger explicit ceiling gives that real workload headroom to grow without
// weakening the truncation-tolerance guarantee for lines even remotely
// close to their normal size.
const maxLineSize = 1024 * 1024

// Append adds r to the ledger as one JSON-lines record, creating the file
// and any missing parent directories as needed. It does not fsync: per the
// package doc, a record lost to a crash before the OS flushes it only
// costs one redundant re-analysis of that file on the next run, so the
// extra durability is not worth paying on every one of what the worker
// pool calls once per analysed file.
func (l *Ledger) Append(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if dir := filepath.Dir(l.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ledger: creating %s: %w", dir, err)
		}
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: opening %s: %w", l.path, err)
	}
	defer f.Close()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("ledger: encoding record for %s: %w", r.FilePath, err)
	}
	line = append(line, '\n')

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("ledger: writing %s: %w", l.path, err)
	}
	return nil
}

// Replay reads every record in the ledger, keeping the last one per
// FilePath -- a later Append always wins over an earlier one for the same
// key. A line that fails to parse as JSON is silently skipped rather than
// treated as an error: the expected cause is a hard kill mid-Append (a
// partial write with no trailing newline), which per the package doc must
// never abort recovery of the valid records around it. A missing ledger
// file is not an error either -- it means nothing has been recorded yet.
func (l *Ledger) Replay() (map[string]Record, error) {
	records := make(map[string]Record)

	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return records, nil
		}
		return nil, fmt.Errorf("ledger: opening %s: %w", l.path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue // truncated/malformed line -- skip, don't fail replay
		}
		records[r.FilePath] = r
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ledger: reading %s: %w", l.path, err)
	}
	return records, nil
}

// NeedsRun reports whether filePath must be (re-)analysed: true if no
// record exists for it yet, or its recorded PackageHash differs from
// currentPackageHash. NeedsRun has no error return -- its callers (the
// queue refill logic) use it directly in a boolean expression -- so a
// Replay error is swallowed and treated as "needs run" rather than
// propagated. That is the deliberately safe direction: the cost of a false
// "needs run" is one redundant, harmless re-analysis, while the cost of a
// false "doesn't need run" would be silently never analysing a file whose
// ledger became unreadable, with nothing to signal that it happened.
func (l *Ledger) NeedsRun(filePath, currentPackageHash string) bool {
	records, err := l.Replay()
	if err != nil {
		return true
	}
	r, ok := records[filePath]
	return !ok || r.PackageHash != currentPackageHash
}
