package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var validLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

func readAll(t *testing.T, p string) []map[string]any {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad JSON %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestEmit_stampsStandardFields(t *testing.T) {
	fixed := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	w.Now = func() time.Time { return fixed }
	if err := w.Emit("info", "run_start", "workspace update started", map[string]any{"projects": 3}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	recs := readAll(t, p)
	if len(recs) != 1 {
		t.Fatalf("want 1 line, got %d", len(recs))
	}
	m := recs[0]
	if m["time"] != fixed.UTC().Format(time.RFC3339Nano) {
		t.Errorf("time = %v", m["time"])
	}
	if m["level"] != "info" || m["msg"] != "workspace update started" {
		t.Errorf("level/msg wrong: %v", m)
	}
	if _, ok := m["projects"]; !ok {
		t.Errorf("caller field dropped: %v", m)
	}
}

func TestEmit_reservedKeysNotOverridable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	_ = w.Emit("warn", "x", "real", map[string]any{"time": "nope", "level": "debug", "msg": "nope", "keep": 1})
	_ = w.Close()
	m := readAll(t, p)[0]
	if m["level"] != "warn" || m["msg"] != "real" {
		t.Errorf("reserved keys overridden: %v", m)
	}
	if _, ok := m["keep"]; !ok {
		t.Errorf("non-reserved field dropped: %v", m)
	}
}

func TestEmit_conformsToJSONLStandard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	_ = w.Emit("info", "run_start", "started", nil)
	_ = w.Emit("error", "run_end", "failed", map[string]any{"failed": 1})
	_ = w.Close()
	for i, m := range readAll(t, p) {
		for _, k := range []string{"time", "level", "msg"} {
			if _, ok := m[k]; !ok {
				t.Errorf("line %d missing %q", i, k)
			}
		}
		if lvl, _ := m["level"].(string); !validLevels[lvl] {
			t.Errorf("line %d bad level %q", i, lvl)
		}
	}
}

// TestNewWithLimit_RotatesWhenOverThreshold verifies that opening a log already
// at/over the size threshold renames it to "<path>.1" and starts a fresh active
// file, so growth is bounded across runs (bead pg2-6z0rm).
func TestNewWithLimit_RotatesWhenOverThreshold(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	old := []byte("{\"old\":1}\n{\"old\":2}\n")
	if err := os.WriteFile(p, old, 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWithLimit(p, int64(len(old))) // size >= threshold => rotate
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("info", "k", "fresh", nil); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	// Backup holds the old content verbatim.
	got, err := os.ReadFile(p + ".1")
	if err != nil {
		t.Fatalf("expected rotated backup %s.1: %v", p, err)
	}
	if string(got) != string(old) {
		t.Errorf("backup content = %q, want %q", got, old)
	}
	// Active file was reopened fresh: exactly the one new line.
	if recs := readAll(t, p); len(recs) != 1 || recs[0]["msg"] != "fresh" {
		t.Errorf("active file should contain only the post-rotation line, got %v", recs)
	}
}

// TestNewWithLimit_NoRotateUnderThreshold verifies that a log under the
// threshold is appended to, not rotated.
func TestNewWithLimit_NoRotateUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, []byte("{\"old\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWithLimit(p, 1<<20) // 1 MiB: well above the tiny file
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Emit("info", "k", "appended", nil)
	_ = w.Close()

	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Errorf("no backup expected under threshold; stat err = %v", err)
	}
	if recs := readAll(t, p); len(recs) != 2 {
		t.Errorf("expected append (2 lines), got %d", len(recs))
	}
}

// TestNewWithLimit_KeepsSingleBackup verifies that repeated rotations retain
// exactly one backup generation (the newest), never accumulating .2/.3/...
func TestNewWithLimit_KeepsSingleBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")

	for i := 0; i < 3; i++ {
		if err := os.WriteFile(p, []byte("{\"gen\":1}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := NewWithLimit(p, 1) // always over threshold => rotate every time
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	}
	if _, err := os.Stat(p + ".2"); !os.IsNotExist(err) {
		t.Errorf("only one backup generation expected, found %s.2", p)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("expected the single backup %s.1: %v", p, err)
	}
}

// TestNewWithLimit_DisabledWhenNonPositive verifies maxBytes <= 0 never rotates.
func TestNewWithLimit_DisabledWhenNonPositive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, []byte("{\"old\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWithLimit(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, err := os.Stat(p + ".1"); !os.IsNotExist(err) {
		t.Errorf("rotation should be disabled for maxBytes<=0; stat err = %v", err)
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var w *Writer // nil
	if err := w.Emit("info", "k", "m", nil); err != nil {
		t.Errorf("nil Emit should be a no-op, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("nil Close should be a no-op, got %v", err)
	}
}

func TestEmit_traceIDOnRunStartAndRunEndOnly(t *testing.T) {
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	w.SetTraceID(id)
	_ = w.Emit("info", "run_start", "started", nil)
	_ = w.Emit("info", "project_result", "one", map[string]any{"name": "a"})
	// A caller-supplied trace_id must not be able to spoof the run's id.
	_ = w.Emit("info", "run_end", "finished", map[string]any{"trace_id": "spoofed"})
	_ = w.Close()

	recs := readAll(t, p)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	if recs[0]["trace_id"] != id {
		t.Errorf("run_start trace_id = %v; want %s", recs[0]["trace_id"], id)
	}
	if _, ok := recs[1]["trace_id"]; ok {
		t.Errorf("project_result must not carry trace_id: %v", recs[1])
	}
	if recs[2]["trace_id"] != id {
		t.Errorf("run_end trace_id = %v; want %s", recs[2]["trace_id"], id)
	}
}

func TestEmit_noTraceIDAddsNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	w.SetTraceID("") // telemetry off
	_ = w.Emit("info", "run_start", "started", nil)
	_ = w.Close()
	if _, ok := readAll(t, p)[0]["trace_id"]; ok {
		t.Fatal("telemetry off: run_start must be byte-identical to today (no trace_id key)")
	}
	var nilW *Writer
	nilW.SetTraceID("x") // must not panic
}
