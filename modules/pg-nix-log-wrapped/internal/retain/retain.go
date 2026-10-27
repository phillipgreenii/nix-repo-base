// Package retain produces the RETAINED log: a filtered copy of the raw JSONL
// nix writes (pg2-sdbhh4.1).
//
// nix writes json-log-path itself, so the wrapper points it at a scratch
// <name>.raw file and copies the lines worth keeping into <name>.jsonl. The
// copy drops only type-105 progress and type-106 results (nixlog.RetainDrop),
// which are about 99% of the bytes, and stops at a size cap (DefaultCap).
// Two pieces share that rule so a run finalized live and a run finalized by
// the directory sweep after a SIGKILL produce the same file: Handler (the
// live tailer handler) and Finalize (the offline filter).
package retain

import (
	"bufio"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/nixlog"
)

const (
	// DefaultCap is the retained-file cap (64 MiB). When it is hit writing
	// stops (Writer.Truncated), but parsing is unaffected.
	DefaultCap int64 = 64 << 20

	// maxLine bounds one line read by Finalize; a longer line is not nix
	// output and is skipped (the live tailer discards the same way).
	maxLine = 16 << 20
)

// Writer appends complete lines to a file, never exceeding a byte cap.
type Writer struct {
	w         *bufio.Writer
	max, n    int64
	truncated bool
}

// NewWriter returns a Writer on w; capBytes <= 0 selects DefaultCap.
func NewWriter(w io.Writer, capBytes int64) *Writer {
	if capBytes <= 0 {
		capBytes = DefaultCap
	}
	return &Writer{w: bufio.NewWriterSize(w, 64<<10), max: capBytes}
}

// WriteLine writes line plus a newline unless that would pass the cap. After
// the first line that does not fit nothing more is written (the file is a
// prefix of the stream, never a prefix with holes).
func (w *Writer) WriteLine(line []byte) bool {
	if w.truncated {
		return false
	}
	need := int64(len(line)) + 1
	if w.n+need > w.max {
		w.truncated = true
		return false
	}
	_, err1 := w.w.Write(line)
	err2 := w.w.WriteByte('\n')
	if err1 != nil || err2 != nil {
		w.truncated = true // a failing disk is treated like a full cap
		return false
	}
	w.n += need
	return true
}

// Truncated reports whether a line was refused because of the cap.
func (w *Writer) Truncated() bool { return w.truncated }

// Flush writes buffered bytes through.
func (w *Writer) Flush() error { return w.w.Flush() }

// Observer is the next stage a Handler feeds (the nixlog Processor, possibly
// behind the override guard).
type Observer interface {
	Line(line []byte, at time.Time)
}

// Handler is a tailer.Handler: it hands EVERY line to Next, writes the lines
// the retained log keeps, and reports their size so the tailer's parse cap
// counts retained bytes rather than raw file offset.
//
// Hook point for pg2-sdbhh4.2 (aggregate progress fold): the dropped
// type-105/106 lines still reach Next, so the in-memory fold belongs in
// nixlog.Processor.Line (its "result" case), not here.
type Handler struct {
	W    *Writer
	Next Observer
}

// Line implements tailer.Handler. It returns the bytes this line counts
// toward the parse cap: zero for a line the retained log drops.
func (h *Handler) Line(line []byte, at time.Time) int {
	if h.Next != nil {
		h.Next.Line(line, at)
	}
	if nixlog.RetainDrop(line) {
		return 0
	}
	h.W.WriteLine(line)
	return len(line) + 1
}

// Flush implements the tailer's optional flush hook (called after each poll).
func (h *Handler) Flush() { _ = h.W.Flush() }

// Finalize filters the raw file at rawPath into outPath (created or
// truncated, mode 0600, never through a symlink) with the same rule as the
// live Handler and the same cap. A trailing line without a newline is
// dropped: its writer is gone, so it will never be completed.
func Finalize(rawPath, outPath string, capBytes int64) (err error) {
	in, err := os.OpenFile(rawPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	w := NewWriter(out, capBytes)
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()

	r := bufio.NewReaderSize(in, 256<<10)
	var line []byte
	over := false
	for {
		frag, rerr := r.ReadSlice('\n')
		if !over {
			if len(line)+len(frag) > maxLine {
				over, line = true, nil
			} else {
				line = append(line, frag...)
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return w.Flush()
			}
			_ = w.Flush()
			return rerr
		}
		if !over {
			l := line[:len(line)-1]
			if n := len(l); n > 0 && l[n-1] == '\r' {
				l = l[:n-1]
			}
			if len(l) > 0 && !nixlog.RetainDrop(l) {
				w.WriteLine(l)
				if w.Truncated() {
					return w.Flush() // cap hit: nothing more will be written
				}
			}
		}
		line, over = line[:0], false
	}
}
