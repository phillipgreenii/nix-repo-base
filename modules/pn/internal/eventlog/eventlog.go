// Package eventlog is pn's structured JSONL event stream, written to a dedicated
// file under ${XDG_STATE_HOME}/pn/, separate from pn's human stdout transcript.
// Lines conform to the phillipgreenii JSONL standard (time/level/msg). The Writer
// is nil-safe: a nil *Writer makes Emit/Close no-ops so logging never breaks a run.
package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is a JSONL event-log writer. Safe for concurrent use; nil-safe.
type Writer struct {
	mu  sync.Mutex
	f   *os.File
	Now func() time.Time // injectable clock; New defaults it to time.Now
}

// New opens (creating parent dirs) the JSONL log at path in append mode.
func New(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, Now: time.Now}, nil
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Emit writes one JSON line. time/level/kind/msg are stamped and cannot be
// overridden by fields. A nil Writer is a no-op.
func (w *Writer) Emit(level, kind, msg string, fields map[string]any) error {
	if w == nil {
		return nil
	}
	rec := make(map[string]any, len(fields)+4)
	for k, v := range fields {
		switch k {
		case "time", "level", "kind", "msg":
		default:
			rec[k] = v
		}
	}
	rec["time"] = w.now().UTC().Format(time.RFC3339Nano)
	rec["level"] = level
	rec["kind"] = kind
	rec["msg"] = msg
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.f.Write(b)
	return err
}

// Close closes the underlying file. A nil Writer is a no-op.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	return w.f.Close()
}
