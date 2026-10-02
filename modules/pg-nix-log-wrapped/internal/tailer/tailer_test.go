package tailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rec struct {
	lines []string
	at    []time.Time
}

func (r *rec) Line(l []byte, at time.Time) {
	r.lines = append(r.lines, string(l))
	r.at = append(r.at, at)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFile(t *testing.T) (*os.File, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "log.jsonl")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, p
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestPartialTrailingLineIsBufferedUntilComplete(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	clk := &clock{time.Now()}
	tl := New(f, r, clk.now, 0)

	appendTo(t, p, "one\ntw")
	tl.Poll()
	if strings.Join(r.lines, ",") != "one" {
		t.Fatalf("after first poll: %q", r.lines)
	}
	appendTo(t, p, "o\nthree\nfo")
	tl.Poll()
	if strings.Join(r.lines, ",") != "one,two,three" {
		t.Fatalf("after second poll: %q", r.lines)
	}
	// The incomplete "fo" is dropped at the end, never emitted.
	stop := make(chan struct{})
	close(stop)
	tl.Run(stop, time.Millisecond, time.Second)
	if strings.Join(r.lines, ",") != "one,two,three" {
		t.Errorf("trailing partial line leaked: %q", r.lines)
	}
}

func TestLinesAreStampedAtReadTime(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	clk := &clock{time.Unix(1000, 0)}
	tl := New(f, r, clk.now, 0)
	appendTo(t, p, "a\n")
	tl.Poll()
	clk.t = time.Unix(2000, 0)
	appendTo(t, p, "b\r\n\n") // CRLF trimmed, empty line skipped
	tl.Poll()
	if len(r.lines) != 2 || r.lines[1] != "b" {
		t.Fatalf("lines = %q", r.lines)
	}
	if !r.at[0].Equal(time.Unix(1000, 0)) || !r.at[1].Equal(time.Unix(2000, 0)) {
		t.Errorf("stamps = %v", r.at)
	}
}

func TestLagIsReadTimeMinusMtimeAndClampedUnderSkew(t *testing.T) {
	f, p := newFile(t)
	base := time.Unix(1_700_000_000, 0)
	clk := &clock{base}
	tl := New(f, &rec{}, clk.now, 0)

	appendTo(t, p, "x\n")
	if err := os.Chtimes(p, base.Add(-250*time.Millisecond), base.Add(-250*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	tl.Poll()
	if got := tl.MaxLag(); got != 250*time.Millisecond {
		t.Errorf("lag = %v, want 250ms", got)
	}

	// Clock skew: the file's mtime is in the FUTURE of the read clock. The lag
	// must never go negative, and must not erase the earlier maximum.
	appendTo(t, p, "y\n")
	future := base.Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	tl.Poll()
	if got := tl.MaxLag(); got != 250*time.Millisecond {
		t.Errorf("lag after skew = %v", got)
	}
	f2, p2 := newFile(t)
	tl2 := New(f2, &rec{}, clk.now, 0)
	appendTo(t, p2, "z\n")
	if err := os.Chtimes(p2, future, future); err != nil {
		t.Fatal(err)
	}
	tl2.Poll()
	if got := tl2.MaxLag(); got != 0 {
		t.Errorf("skewed lag = %v, want 0", got)
	}
}

func TestSizeCapStopsIngestingButKeepsDraining(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	clk := &clock{time.Now()}
	tl := New(f, r, clk.now, 20) // 20-byte cap
	appendTo(t, p, "aaaa\nbbbb\ncccc\ndddd\neeee\nffff\n")
	tl.Poll()
	if !tl.Truncated() {
		t.Fatal("cap not detected")
	}
	n := len(r.lines)
	if n == 0 || n >= 6 {
		t.Fatalf("expected some but not all lines, got %d: %q", n, r.lines)
	}
	// More data after the cap is consumed without parsing.
	appendTo(t, p, "gggg\nhhhh\n")
	if consumed := tl.Poll(); consumed == 0 {
		t.Error("tailer must keep draining past the cap")
	}
	if len(r.lines) != n {
		t.Errorf("parsed after cap: %q", r.lines)
	}
	if tl.Poll() != 0 {
		t.Error("nothing left to drain")
	}
}

func TestFileTruncationRestartsAtZero(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	tl := New(f, r, time.Now, 0)
	appendTo(t, p, "first\nsecond\n")
	tl.Poll()
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	appendTo(t, p, "new\n")
	tl.Poll()
	if got := strings.Join(r.lines, ","); got != "first,second,new" {
		t.Errorf("lines = %q", got)
	}
}

func TestRunDrainsAfterStop(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	tl := New(f, r, time.Now, 0)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { tl.Run(stop, 5*time.Millisecond, 2*time.Second); close(done) }()
	appendTo(t, p, "early\n")
	// Data written right before stop must still be read by the final drain.
	appendTo(t, p, "late\n")
	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after stop")
	}
	if got := strings.Join(r.lines, ","); got != "early,late" {
		t.Errorf("lines = %q", got)
	}
}

func TestHugeUnterminatedLineIsDiscarded(t *testing.T) {
	f, p := newFile(t)
	r := &rec{}
	tl := New(f, r, time.Now, 0)
	appendTo(t, p, strings.Repeat("x", maxPartial+1))
	tl.Poll()
	appendTo(t, p, "\nok\n")
	tl.Poll()
	if got := strings.Join(r.lines, ","); got != "ok" {
		t.Errorf("lines = %q", got)
	}
}
