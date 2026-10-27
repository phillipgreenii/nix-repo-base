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

	// DefaultMaxBytes is the W-12 parse cap (256 MiB of RETAINED bytes: those
	// the handler reports, not the raw file offset).
	DefaultMaxBytes int64 = 256 << 20
	// DefaultInterval is the poll period; the Phase 1 spike measured lag of
	// about one such interval.
	DefaultInterval = 20 * time.Millisecond
)

// Handler receives each complete line (without its newline) and the read-time
// stamp. The slice is only valid during the call. It returns how many bytes
// the line counts toward the parse cap (zero for a line the retained log
// drops, such as progress); the tailer keeps that running total separately
// from the file offset.
type Handler interface {
	Line(line []byte, at time.Time) (counted int)
}

// flusher is an optional Handler capability: Flush is called at the end of
// every Poll so a buffering handler never holds data across a poll.
type flusher interface{ Flush() }

// Tailer follows one file. It is used by a single goroutine at a time.
type Tailer struct {
	f        *os.File
	h        Handler
	now      func() time.Time
	maxBytes int64

	offset    int64
	retained  int64 // bytes the handler counted; compared with maxBytes
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

// Truncated reports whether the parse cap was hit; parsing stopped then.
func (t *Tailer) Truncated() bool { return t.truncated }

// MaxLag is the largest read-time-minus-mtime observed, never negative.
func (t *Tailer) MaxLag() time.Duration { return t.maxLag }

// Poll reads whatever has been appended since the last call and returns the
// number of bytes consumed from the file (including bytes skipped past the
// parse cap).
func (t *Tailer) Poll() int64 {
	if f, ok := t.h.(flusher); ok {
		defer f.Flush()
	}
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
		if t.truncated {
			// Past the parse cap: keep draining without parsing.
			t.offset, t.buf = size, nil
			break
		}
		want := int64(len(chunk))
		if rem := size - t.offset; rem < want {
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
	if t.truncated {
		return
	}
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
			if t.retained >= t.maxBytes {
				// The parse cap counts retained bytes only: raw progress
				// never gets here, so stop and failure events survive a
				// long run. Once hit, parsing stops for good.
				t.truncated = true
				t.buf = nil
				return
			}
			t.retained += int64(t.h.Line(line, at))
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
