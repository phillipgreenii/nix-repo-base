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

func TestNilLoggerIsSafe(t *testing.T) {
	var w *Writer // nil
	if err := w.Emit("info", "k", "m", nil); err != nil {
		t.Errorf("nil Emit should be a no-op, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("nil Close should be a no-op, got %v", err)
	}
}
