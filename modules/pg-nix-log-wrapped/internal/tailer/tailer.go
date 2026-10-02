// Package tailer reads a growing JSONL file and hands complete lines to a
// handler (W-7, W-12). It polls (nix appends to a plain file, so there is no
// reader to block on), buffers an incomplete trailing line, stamps each
// complete line with the time it was READ, and tracks the lag between that
// read and the file's modification time.
package tailer

import (
	"bytes"
	"io"
	"os"
	"time"
)

const (
	chunkSize = 256 << 10
	// maxPartial bounds the buffered incomplete line. A line this long is not
	// nix output; it is discarded rather than allowed to grow without bound.
	maxPartial = 16 << 20

	// DefaultMaxBytes is the W-12 ingest cap (256 MiB).
	DefaultMaxBytes int64 = 256 << 20
	// DefaultInterval is the poll period; the Phase 1 spike measured lag of
	// about one such interval.
	DefaultInterval = 20 * time.Millisecond
)

// Handler receives each complete line (without its newline) and the read-time
// stamp. The slice is only valid during the call.
type Handler interface {
	Line(line []byte, at time.Time)
}

// Tailer follows one file. It is used by a single goroutine at a time.
type Tailer struct {
	f        *os.File
	h        Handler
	now      func() time.Time
	maxBytes int64

	offset    int64
	buf       []byte
	chunk     []byte
	truncated bool
	maxLag    time.Duration
}

// New returns a Tailer reading f from offset 0. now stamps lines; maxBytes <= 0
// selects DefaultMaxBytes.
func New(f *os.File, h Handler, now func() time.Time, maxBytes int64) *Tailer {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Tailer{f: f, h: h, now: now, maxBytes: maxBytes}
}

// Truncated reports whether the size cap was hit; ingestion stopped then.
func (t *Tailer) Truncated() bool { return t.truncated }

// MaxLag is the largest read-time-minus-mtime observed, never negative.
func (t *Tailer) MaxLag() time.Duration { return t.maxLag }

// Poll reads whatever has been appended since the last call and returns the
// number of bytes consumed from the file (including bytes skipped past the
// cap).
func (t *Tailer) Poll() int64 {
	fi, err := t.f.Stat()
	if err != nil {
		return 0
	}
	size := fi.Size()
	if size < t.offset { // someone truncated the file: start over
		t.offset, t.buf = 0, t.buf[:0]
	}
	start := t.offset
	if t.chunk == nil {
		t.chunk = make([]byte, chunkSize)
	}
	chunk := t.chunk
	for t.offset < size {
		if t.truncated || t.offset >= t.maxBytes {
			// Past the cap: keep draining without parsing.
			t.truncated = true
			t.offset, t.buf = size, nil
			break
		}
		want := int64(len(chunk))
		if rem := size - t.offset; rem < want {
			want = rem
		}
		if rem := t.maxBytes - t.offset; rem < want {
			want = rem
		}
		n, err := t.f.ReadAt(chunk[:want], t.offset)
		if n > 0 {
			t.offset += int64(n)
			t.consume(chunk[:n], fi.ModTime())
		}
		if err != nil && err != io.EOF {
			break
		}
		if n == 0 {
			break
		}
	}
	return t.offset - start
}

func (t *Tailer) consume(data []byte, mtime time.Time) {
	at := t.now()
	if lag := at.Sub(mtime); lag > t.maxLag {
		t.maxLag = lag
	}
	t.buf = append(t.buf, data...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSuffix(t.buf[:i], []byte{'\r'})
		if len(line) > 0 {
			t.h.Line(line, at)
		}
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > maxPartial {
		t.buf = nil
	} else if len(t.buf) == 0 {
		t.buf = nil
	} else {
		// Detach from the (growing) backing array so consumed bytes are freed.
		t.buf = append([]byte(nil), t.buf...)
	}
}

// Run polls every interval until stop is closed, then performs a final drain
// bounded by drainFor and returns. The incomplete trailing line, if any, is
// dropped: nix has exited, so it will never be completed.
func (t *Tailer) Run(stop <-chan struct{}, interval, drainFor time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			deadline := time.Now().Add(drainFor)
			for t.Poll() > 0 && time.Now().Before(deadline) {
			}
			return
		case <-tick.C:
			t.Poll()
		}
	}
}
