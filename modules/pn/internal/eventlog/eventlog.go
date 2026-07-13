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

// DefaultMaxBytes is the size at or above which New rotates the event log when
// it is (re)opened. Without a cap the O_APPEND JSONL grew without bound — every
// `pn workspace update` appended forever (bead pg2-6z0rm). The log is also
// shipped to Loki, so on-disk retention only needs to bridge collector scrapes;
// 5 MiB keeps plenty of local history while bounding disk use to ~2x this (the
// active file plus one backup).
const DefaultMaxBytes int64 = 5 << 20 // 5 MiB

// New opens (creating parent dirs) the JSONL log at path in append mode,
// rotating first if it has already reached DefaultMaxBytes.
func New(path string) (*Writer, error) {
	return NewWithLimit(path, DefaultMaxBytes)
}

// NewWithLimit is New with an explicit rotation threshold. A maxBytes <= 0
// disables rotation. Rotation is size-based and collector-friendly: pn runs are
// short-lived, so checking on open bounds growth across runs without mid-run
// races. When the existing log is at or over the threshold it is renamed to
// "<path>.1" (atomically replacing any previous backup, so exactly one backup
// generation is kept) and a fresh log is opened at the stable path — a tailing
// collector (promtail/Loki) follows the rename by inode and resumes on the new
// file. A rotation failure is non-fatal: logging continues on the existing file.
func NewWithLimit(path string, maxBytes int64) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if maxBytes > 0 {
		if fi, err := os.Stat(path); err == nil && fi.Size() >= maxBytes {
			_ = os.Rename(path, path+".1")
		}
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
