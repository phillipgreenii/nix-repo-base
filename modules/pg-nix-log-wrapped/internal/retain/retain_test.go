package retain

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	start    = `{"action":"start","id":1,"level":0,"parent":0,"text":"building","type":105}`
	progress = `{"action":"result","fields":[1,2,3,4],"id":1,"type":105}`
	buildLog = `{"action":"result","fields":["compiling"],"id":1,"type":101}`
	expected = `{"action":"result","fields":[104,1,2],"id":1,"type":106}`
	msg      = `{"action":"msg","level":0,"msg":"error: boom"}`
	stop     = `{"action":"stop","id":1}`
)

type sink struct{ lines []string }

func (s *sink) Line(l []byte, _ time.Time) { s.lines = append(s.lines, string(l)) }

func TestHandlerWritesKeptLinesForwardsEverythingAndCountsKeptBytes(t *testing.T) {
	var buf bytes.Buffer
	next := &sink{}
	h := &Handler{W: NewWriter(&buf, 0), Next: next}
	counted := 0
	all := []string{start, progress, buildLog, expected, msg, stop}
	for _, l := range all {
		counted += h.Line([]byte(l), time.Now())
	}
	h.Flush()
	if got, want := buf.String(), strings.Join([]string{start, buildLog, msg, stop}, "\n")+"\n"; got != want {
		t.Errorf("retained:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(next.lines, "\n") != strings.Join(all, "\n") {
		t.Errorf("Next must see every line (the aggregate-progress fold needs the dropped ones): %v", next.lines)
	}
	if counted != buf.Len() {
		t.Errorf("counted %d bytes, retained %d", counted, buf.Len())
	}
}

func TestHandlerKeepsCountingAfterTheRetainedCapIsHit(t *testing.T) {
	var buf bytes.Buffer
	next := &sink{}
	capBytes := int64(len(start) + 1)
	h := &Handler{W: NewWriter(&buf, capBytes), Next: next}
	h.Line([]byte(start), time.Now())
	if h.W.Truncated() {
		t.Fatal("start fits exactly")
	}
	if n := h.Line([]byte(msg), time.Now()); n != len(msg)+1 {
		t.Errorf("a kept line is counted for the parse cap even when not written: %d", n)
	}
	h.Line([]byte(stop), time.Now()) // shorter than msg, but the writer stays stopped
	h.Flush()
	if int64(buf.Len()) > capBytes || buf.String() != start+"\n" {
		t.Errorf("retained %q exceeds the cap of %d", buf.String(), capBytes)
	}
	if !h.W.Truncated() {
		t.Error("cap hit not reported")
	}
	if len(next.lines) != 3 {
		t.Errorf("parsing must continue past the retained cap: %v", next.lines)
	}
}

func TestFinalizeMatchesTheLiveRuleAndSkipsPartialAndEmptyLines(t *testing.T) {
	dir := t.TempDir()
	raw, out := filepath.Join(dir, "x.raw"), filepath.Join(dir, "x.jsonl")
	body := strings.Join([]string{start, progress, "", buildLog + "\r", expected, msg, stop}, "\n") + "\n" + `{"action":"sto`
	if err := os.WriteFile(raw, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Finalize(raw, out, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if want := strings.Join([]string{start, buildLog, msg, stop}, "\n") + "\n"; string(got) != want {
		t.Errorf("finalized:\n%s\nwant:\n%s", got, want)
	}
	// Live and offline paths agree byte for byte.
	var buf bytes.Buffer
	h := &Handler{W: NewWriter(&buf, 0)}
	for _, l := range []string{start, progress, buildLog, expected, msg, stop} {
		h.Line([]byte(l), time.Now())
	}
	h.Flush()
	if buf.String() != string(got) {
		t.Error("live handler and Finalize disagree")
	}
}

func TestFinalizeSkipsAnOverlongLineAndKeepsGoing(t *testing.T) {
	dir := t.TempDir()
	raw, out := filepath.Join(dir, "x.raw"), filepath.Join(dir, "x.jsonl")
	huge := `{"action":"msg","msg":"` + strings.Repeat("x", maxLine) + `"}`
	if err := os.WriteFile(raw, []byte(start+"\n"+huge+"\n"+stop+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Finalize(raw, out, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != start+"\n"+stop+"\n" {
		t.Errorf("got %d bytes", len(got))
	}
}

func TestFinalizeRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(dir, "x.raw")
	if err := os.WriteFile(raw, []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "x.jsonl")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := Finalize(raw, link, 0); err == nil {
		t.Error("an output symlink must be refused")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Error("symlink target was written")
	}
	rawLink := filepath.Join(dir, "y.raw")
	if err := os.Symlink(victim, rawLink); err != nil {
		t.Fatal(err)
	}
	if err := Finalize(rawLink, filepath.Join(dir, "y.jsonl"), 0); err == nil {
		t.Error("an input symlink must be refused")
	}
}
