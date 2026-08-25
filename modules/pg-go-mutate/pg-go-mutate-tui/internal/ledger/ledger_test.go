package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendThenReplayRoundTrips(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	r := Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done", Timestamp: time.Now()}
	if err := l.Append(r); err != nil {
		t.Fatal(err)
	}
	records, err := l.Replay()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := records["pkg/a.go"]
	if !ok || got.PackageHash != "abc" || got.Status != "done" {
		t.Fatalf("replay did not recover the appended record: %+v", got)
	}
}

func TestReplayKeepsLastRecordPerKey(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "failed"})
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "def", Status: "done"})
	records, _ := l.Replay()
	if records["pkg/a.go"].Status != "done" || records["pkg/a.go"].PackageHash != "def" {
		t.Fatalf("expected the SECOND record to win, got %+v", records["pkg/a.go"])
	}
}

func TestReplayToleratesTruncatedTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	l := New(path)
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done"})
	// Simulate a SIGKILL mid-append: a second, truncated line with no
	// trailing newline and invalid JSON.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"file_path":"pkg/b.go","status":"do`)
	f.Close()
	records, err := l.Replay()
	if err != nil {
		t.Fatalf("replay must tolerate a truncated trailing line, got error: %v", err)
	}
	if _, ok := records["pkg/b.go"]; ok {
		t.Fatal("truncated record must not appear in replay results")
	}
	if records["pkg/a.go"].Status != "done" {
		t.Fatal("a valid earlier record must still survive replay")
	}
}

func TestNeedsRun(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ledger.jsonl"))
	if !l.NeedsRun("pkg/a.go", "abc") {
		t.Fatal("a file with no record must need a run")
	}
	l.Append(Record{FilePath: "pkg/a.go", PackageHash: "abc", Status: "done"})
	if l.NeedsRun("pkg/a.go", "abc") {
		t.Fatal("an unchanged hash must not need a run")
	}
	if !l.NeedsRun("pkg/a.go", "different") {
		t.Fatal("a changed hash must need a run")
	}
}
