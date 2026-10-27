package nixlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type collect struct{ acts []Activity }

func (c *collect) Emit(a Activity) { c.acts = append(c.acts, a) }

func (c *collect) byKind(k Kind) []Activity {
	var out []Activity
	for _, a := range c.acts {
		if a.Kind == k {
			out = append(out, a)
		}
	}
	return out
}

func (c *collect) named(name string) (Activity, bool) {
	for _, a := range c.acts {
		if a.Name == name {
			return a, true
		}
	}
	return Activity{}, false
}

var t0 = time.Unix(1_800_000_000, 0)

// feedFile streams a golden through a Processor, stamping line i at t0+i ms.
// It does NOT call Finish.
func feedFile(t *testing.T, name string, opts Options) (*Processor, *collect, int) {
	t.Helper()
	return feedFileStep(t, name, opts, time.Millisecond)
}

func feedFileStep(t *testing.T, name string, opts Options, step time.Duration) (*Processor, *collect, int) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	c := &collect{}
	p := NewProcessor(c, opts)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for sc.Scan() {
		p.Line(sc.Bytes(), t0.Add(time.Duration(n)*step))
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return p, c, n
}

func TestGoldensMatchADRFixtures(t *testing.T) {
	adr := filepath.Join("..", "..", "..", "..", "docs", "adr", "0028-fixtures")
	if _, err := os.Stat(adr); err != nil {
		t.Skip("ADR 0028 fixtures not in this source tree (nix sandbox)")
	}
	entries, _ := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if len(entries) == 0 {
		t.Fatal("no goldens in testdata")
	}
	for _, e := range entries {
		want, err := os.ReadFile(filepath.Join(adr, filepath.Base(e)))
		if err != nil {
			t.Errorf("%s: no ADR counterpart: %v", e, err)
			continue
		}
		got, _ := os.ReadFile(e)
		if !bytes.Equal(got, want) {
			t.Errorf("%s drifted from docs/adr/0028-fixtures", e)
		}
	}
}

func TestLocalBuildGolden(t *testing.T) {
	p, c, n := feedFile(t, "local-build.jsonl", Options{})
	if len(c.acts) != 0 {
		t.Fatalf("a successful build must be held until Finish, got %d emitted", len(c.acts))
	}
	end := t0.Add(time.Hour)
	p.Finish(end)
	builds := c.byKind(KindBuild)
	if len(builds) != 1 || len(c.acts) != 1 {
		t.Fatalf("want exactly one build activity, got %+v", c.acts)
	}
	b := builds[0]
	if b.Name != "kq2-slow-1790833925" || b.Failed {
		t.Errorf("build = %+v", b)
	}
	if !strings.HasSuffix(b.Path, "-kq2-slow-1790833925.drv") {
		t.Errorf("path = %q", b.Path)
	}
	if !b.End.After(b.Start) || b.End.After(t0.Add(time.Duration(n)*time.Millisecond)) {
		t.Errorf("stamps wrong: %v -> %v", b.Start, b.End)
	}
	if s := p.Summary(); s.PlanBuild != 1 || s.PlanFetch != 0 {
		t.Errorf("plan = %+v", s)
	}
}

// kept returns the ids whose progress the Processor is meant to keep when the
// stream is read start to end: aggregate (103/104) activities, substitutions
// (108) and CopyPath (100) children of a substitution. It is a deliberately
// independent re-derivation (encoding/json, no byte tricks).
func keptIDs(t *testing.T, raw []byte) map[uint64]bool {
	t.Helper()
	kept := map[uint64]bool{}
	subs := map[uint64]bool{}
	for _, l := range bytes.Split(raw, []byte("\n")) {
		if len(l) == 0 {
			continue
		}
		var ev struct {
			Action string `json:"action"`
			ID     uint64 `json:"id"`
			Parent uint64 `json:"parent"`
			Type   int    `json:"type"`
		}
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Action != "start" {
			continue
		}
		switch ev.Type {
		case 103, 104:
			kept[ev.ID] = true
		case 108:
			kept[ev.ID], subs[ev.ID] = true, true
		case 100:
			if subs[ev.Parent] {
				kept[ev.ID] = true
			}
		}
	}
	return kept
}

func TestProgressOfKeptActivitiesIsFoldedAndAllOtherResultsDropped(t *testing.T) {
	for _, name := range []string{"cached-fetch.jsonl", "chatty-build.trimmed.jsonl", "local-build.jsonl", "flake-check.jsonl", "failed-build-keep-going.jsonl"} {
		raw, _ := os.ReadFile(filepath.Join("testdata", name))
		kept := keptIDs(t, raw)
		wantDrop, wantFold := 0, 0
		for _, l := range bytes.Split(raw, []byte("\n")) {
			if len(l) == 0 {
				continue
			}
			var ev struct {
				Action string `json:"action"`
				ID     uint64 `json:"id"`
				Type   int    `json:"type"`
			}
			_ = json.Unmarshal(l, &ev)
			switch {
			case ev.Action == "result" && ev.Type == 101:
				wantDrop++
			case ev.Action == "result" && ev.Type == 105 && kept[ev.ID]:
				wantFold++
			case ev.Action == "result" && ev.Type == 105:
				wantDrop++
			}
		}
		p, _, n := feedFile(t, name, Options{})
		s := p.Summary()
		if s.Dropped != wantDrop || s.Folded != wantFold {
			t.Errorf("%s: dropped %d folded %d, want %d / %d", name, s.Dropped, s.Folded, wantDrop, wantFold)
		}
		if s.Lines != n || s.Malformed != 0 {
			t.Errorf("%s: lines=%d malformed=%d", name, s.Lines, s.Malformed)
		}
	}
	// The fixtures must actually exercise both branches.
	p, _, _ := feedFile(t, "chatty-build.trimmed.jsonl", Options{})
	if s := p.Summary(); s.Dropped == 0 {
		t.Error("chatty-build must have dropped lines")
	}
	p, _, _ = feedFile(t, "flake-check.jsonl", Options{})
	if s := p.Summary(); s.Folded == 0 {
		t.Error("flake-check must have folded progress")
	}
}

func TestSuffixIDAndFastFieldsAreConservative(t *testing.T) {
	ids := []struct {
		line string
		id   uint64
		ok   bool
	}{
		{`{"action":"result","fields":[0,0,0,0],"id":1,"type":105}`, 1, true},
		{`{"action":"result","fields":[2,3,0,2],"id":43958990274602,"type":105}`, 43958990274602, true},
		{`{"action":"result","fields":[1,2],"id":7,"type":105}`, 7, true},
		{`{"action":"result","fields":[1,2],"id":,"type":105}`, 0, false},
		{`{"action":"result","fields":[1,2],"id":7x,"type":105}`, 0, false},
		{`{"action":"result","fields":[1,2],"id":99999999999999999999,"type":105}`, 0, false},
		{`{"action":"result","fields":[1,2],"type":105}`, 0, false},
		{`{"action":"result","fields":[1,2],"id":7 ,"type":105}`, 0, false},
		{`,"type":105}`, 0, false},
		{`"type":105}`, 0, false},
	}
	for _, c := range ids {
		if !bytes.HasSuffix([]byte(c.line), suffix105) {
			t.Fatalf("bad case %s", c.line)
		}
		id, ok := suffixID([]byte(c.line))
		if id != c.id || ok != c.ok {
			t.Errorf("suffixID(%s) = %d,%v", c.line, id, ok)
		}
	}
	fields := []struct {
		line string
		want [4]int64
		ok   bool
	}{
		{`{"action":"result","fields":[2,3,0,2],"id":1,"type":105}`, [4]int64{2, 3, 0, 2}, true},
		{`{"action":"result","fields":[10],"id":1,"type":105}`, [4]int64{10}, true},
		{`{"action":"result","fields":[],"id":1,"type":105}`, [4]int64{}, false},
		{`{"action":"result","fields":[1,2,3,4,5],"id":1,"type":105}`, [4]int64{}, false},
		{`{"action":"result","fields":[1.5,2],"id":1,"type":105}`, [4]int64{}, false},
		{`{"action":"result","fields":[-1,2],"id":1,"type":105}`, [4]int64{}, false},
		{`{"action":"result","fields":["x"],"id":1,"type":105}`, [4]int64{}, false},
		{`{"action":"result","fields":[1, 2],"id":1,"type":105}`, [4]int64{}, false},
	}
	for _, c := range fields {
		got, ok := fastFields([]byte(c.line))
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("fastFields(%s) = %v,%v", c.line, got, ok)
		}
	}
}

func progressLine(id uint64, f ...int) string {
	parts := make([]string, len(f))
	for i, v := range f {
		parts[i] = strconv.Itoa(v)
	}
	return fmt.Sprintf(`{"action":"result","fields":[%s],"id":%d,"type":105}`, strings.Join(parts, ","), id)
}

func startLine(id, parent uint64, typ int) string {
	return fmt.Sprintf(`{"action":"start","id":%d,"level":0,"parent":%d,"text":"","type":%d}`, id, parent, typ)
}

func feedLines(p *Processor, lines ...string) {
	for _, l := range lines {
		p.Line([]byte(l), t0)
	}
}

func TestReorderedProgressKeysAreStillSafe(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	feedLines(p, startLine(1, 0, 104))
	// Key order is not relied on: a reordered progress result for a kept
	// activity is still folded (through the full decode), and one for any
	// other activity produces nothing and is not counted as fast-dropped.
	feedLines(
		p,
		`{"type":105,"id":1,"action":"result","fields":[4,5,0,1]}`,
		`{"type":105,"id":9,"action":"result","fields":[0,0,0,0]}`,
	)
	p.Finish(t0)
	s := p.Summary()
	if !s.HasBuilds || s.Builds != (Progress{Done: 4, Expected: 5, Failed: 1}) {
		t.Errorf("reordered fold: %+v", s)
	}
	if s.Folded != 1 || s.Dropped != 0 || len(c.acts) != 0 {
		t.Errorf("counters/acts: %+v %+v", s, c.acts)
	}
}

func TestOddProgressShapeForKeptActivityKeepsPreviousValue(t *testing.T) {
	p := NewProcessor(&collect{}, Options{})
	feedLines(
		p, startLine(1, 0, 104), progressLine(1, 2, 3, 0, 0),
		`{"action":"result","fields":[1.5,2],"id":1,"type":105}`, // not an integer: ignored
		`{"action":"result","fields":[],"id":1,"type":105}`,
		`{"action":"result","fields":[1,2,3,4,5],"id":1,"type":105}`,
	)
	if s := p.Summary(); s.Builds != (Progress{Done: 2, Expected: 3}) || s.Malformed != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestFlakeCheckAggregateProgress(t *testing.T) {
	p, _, _ := feedFile(t, "flake-check.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	s := p.Summary()
	if !s.HasBuilds || s.Builds != (Progress{Done: 2, Expected: 3, Failed: 0}) {
		t.Errorf("builds = %+v (has %v), want done 2 expected 3 failed 0", s.Builds, s.HasBuilds)
	}
	if !s.HasFetch || s.Fetch != (Progress{}) {
		t.Errorf("fetch = %+v (has %v), want seen with zeros", s.Fetch, s.HasFetch)
	}
}

func TestKeepGoingProgressDiffersFromPlanMessage(t *testing.T) {
	p, _, _ := feedFile(t, "failed-build-keep-going.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	s := p.Summary()
	if s.PlanBuild != 4 {
		t.Errorf("plan = %d, the message count must stay 4", s.PlanBuild)
	}
	if !s.HasBuilds || s.Builds != (Progress{Done: 2, Expected: 3, Failed: 2}) {
		t.Errorf("builds = %+v, want done 2 expected 3 failed 2", s.Builds)
	}
}

func TestSiblingAggregatesAreSummed(t *testing.T) {
	// Real shape (2026-10-26 log): three siblings (parent 0) of each type, two
	// of them ending [0,0,0,0].
	p := NewProcessor(&collect{}, Options{})
	feedLines(
		p,
		startLine(10, 0, 104), startLine(11, 0, 103),
		startLine(20, 0, 104), startLine(21, 0, 103),
		startLine(30, 0, 104), startLine(31, 0, 103),
		progressLine(10, 0, 0, 0, 0), progressLine(11, 0, 0, 0, 0),
		progressLine(20, 0, 0, 0, 0), progressLine(21, 0, 0, 0, 0),
		progressLine(30, 100, 266, 3, 0), progressLine(31, 502, 502, 0, 0),
		progressLine(30, 266, 266, 0, 1), // only the LAST value counts
		`{"action":"stop","id":10}`, `{"action":"stop","id":30}`,
	)
	s := p.Summary()
	if s.Builds != (Progress{Done: 266, Expected: 266, Failed: 1}) || !s.HasBuilds {
		t.Errorf("builds = %+v", s.Builds)
	}
	if s.Fetch != (Progress{Done: 502, Expected: 502}) || !s.HasFetch {
		t.Errorf("fetch = %+v", s.Fetch)
	}
	// And a second sibling with real values adds up.
	feedLines(p, progressLine(20, 5, 7, 0, 2))
	if s := p.Summary(); s.Builds != (Progress{Done: 271, Expected: 273, Failed: 3}) {
		t.Errorf("summed builds = %+v", s.Builds)
	}
}

func TestNestedAggregateCountsOnlyTheOuterOne(t *testing.T) {
	p := NewProcessor(&collect{}, Options{})
	feedLines(
		p,
		startLine(1, 0, 104), // outer builds
		startLine(2, 1, 104), // nested builds under it
		startLine(3, 2, 104), // grand-nested: parent is itself nested
		startLine(4, 1, 103), // fetches nested under the outer builds
		startLine(5, 0, 103), // outer fetches
		progressLine(1, 5, 6, 0, 1), progressLine(2, 3, 3, 0, 0), progressLine(3, 9, 9, 0, 9),
		progressLine(4, 8, 8, 0, 0), progressLine(5, 7, 7, 0, 0),
	)
	s := p.Summary()
	if s.Builds != (Progress{Done: 5, Expected: 6, Failed: 1}) {
		t.Errorf("builds = %+v, want only the outer activity", s.Builds)
	}
	if s.Fetch != (Progress{Done: 7, Expected: 7}) {
		t.Errorf("fetch = %+v, want only the outer activity", s.Fetch)
	}
}

func TestNoProgressLinesMeansNothingSeenAndPlanStillWorks(t *testing.T) {
	p := NewProcessor(&collect{}, Options{})
	feedLines(
		p,
		`{"action":"msg","level":3,"msg":"these 3 derivations will be built:"}`,
		`{"action":"msg","level":3,"msg":"these 5 paths will be fetched (1 MiB download):"}`,
		startLine(1, 0, 104), startLine(2, 0, 103), // aggregates that never report
	)
	s := p.Summary()
	if s.HasBuilds || s.HasFetch || s.Builds != (Progress{}) || s.Fetch != (Progress{}) {
		t.Errorf("progress reported without any progress line: %+v", s)
	}
	if s.PlanBuild != 3 || s.PlanFetch != 5 {
		t.Errorf("plan = %+v", s)
	}
	// A real log with its progress lines removed: plan only.
	raw, _ := os.ReadFile(filepath.Join("testdata", "local-build.jsonl"))
	p2 := NewProcessor(&collect{}, Options{})
	for _, l := range bytes.Split(raw, []byte("\n")) {
		if len(l) > 0 && !RetainDrop(l) {
			p2.Line(l, t0)
		}
	}
	if s := p2.Summary(); s.HasBuilds || s.HasFetch || s.PlanBuild != 1 {
		t.Errorf("local-build without progress: %+v", s)
	}
}

func TestDownloadProgressOfOtherActivitiesIsNeverDecoded(t *testing.T) {
	// A download (type 101 FileTransfer, a child of a type-100 CopyPath that is
	// NOT under a substitution) emits thousands of progress lines.
	p := NewProcessor(&collect{}, Options{})
	feedLines(p, startLine(1, 0, 104), startLine(100, 0, 100), startLine(101, 100, 101))
	const n = 5000
	line := []byte(progressLine(101, 1234567, 99999999, 0, 0))
	for i := 0; i < n; i++ {
		p.Line(line, t0)
		if i%1000 == 0 {
			p.Line([]byte(progressLine(1, i, n, 0, 0)), t0) // aggregate progress interleaved
		}
	}
	s := p.Summary()
	if s.Dropped != n || s.Folded != 5 {
		t.Errorf("dropped %d folded %d, want %d / 5", s.Dropped, s.Folded, n)
	}
	if s.Malformed != 0 || s.Builds.Done != 4000 {
		t.Errorf("summary = %+v", s)
	}
	// The skip path must not even allocate (no decode, no copy).
	if a := testing.AllocsPerRun(200, func() { p.Line(line, t0) }); a != 0 {
		t.Errorf("a skipped progress line allocates %v times", a)
	}
}

func BenchmarkSkippedProgressLine(b *testing.B) {
	p := NewProcessor(&collect{}, Options{})
	p.Line([]byte(startLine(1, 0, 104)), t0)
	line := []byte(progressLine(101, 1234567, 99999999, 0, 0))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.Line(line, t0)
	}
}

func subStart(id uint64) string {
	return fmt.Sprintf(`{"action":"start","fields":["/nix/store/9m2blbp495aa5rxslryxhjx3hjsnd421-x","https://cache.nixos.org"],"id":%d,"level":0,"parent":0,"text":"","type":108}`, id)
}

func TestSubstituteBytesFromItsCopyPathChild(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	feedLines(
		p,
		subStart(7),
		startLine(8, 7, 100), // nix reports a substitution's bytes on this child
		startLine(9, 8, 101), // the download under it: never kept
		progressLine(8, 100, 400, 0, 0),
		progressLine(9, 5, 5, 0, 0),
		progressLine(8, 400, 400, 0, 0),
		`{"action":"stop","id":8}`,
		`{"action":"stop","id":7}`,
		progressLine(8, 999, 999, 0, 0), // late: the activity is gone
	)
	if len(c.acts) != 1 || !c.acts[0].HasBytes || c.acts[0].Bytes != 400 {
		t.Fatalf("acts = %+v", c.acts)
	}
	if s := p.Summary(); s.Folded != 2 || s.Dropped != 2 {
		t.Errorf("summary = %+v", s)
	}
}

func TestSubstituteBytesFromItsOwnProgressAndAbsentWhenNone(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	feedLines(p, subStart(1), subStart(2), startLine(3, 0, 100), // 3 is not under a substitution
		progressLine(1, 10, 20, 0, 0), progressLine(3, 77, 77, 0, 0),
		`{"action":"stop","id":1}`, `{"action":"stop","id":2}`)
	byID := map[int]Activity{}
	for i, a := range c.acts {
		byID[i] = a
	}
	if !byID[0].HasBytes || byID[0].Bytes != 10 {
		t.Errorf("first substitution = %+v", byID[0])
	}
	if byID[1].HasBytes || byID[1].Bytes != 0 {
		t.Errorf("a substitution with no progress must report none: %+v", byID[1])
	}
	// A substitution still open at the end keeps its last bytes.
	c2 := &collect{}
	p2 := NewProcessor(c2, Options{})
	feedLines(p2, subStart(5), progressLine(5, 3, 9, 0, 0))
	p2.Finish(t0)
	if len(c2.acts) != 1 || !c2.acts[0].Failed || c2.acts[0].Bytes != 3 || !c2.acts[0].HasBytes {
		t.Errorf("aborted substitution = %+v", c2.acts)
	}
}

func TestCachedFetchGolden(t *testing.T) {
	p, c, _ := feedFile(t, "cached-fetch.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	subs := c.byKind(KindSubstitute)
	if len(subs) != 4 {
		t.Fatalf("want 4 substitutions, got %d", len(subs))
	}
	for _, s := range subs {
		if s.Server != "cache.nixos.org" || s.Failed || s.NoSpan {
			t.Errorf("substitution = %+v", s)
		}
		if strings.Contains(s.Name, "/nix/store") || len(s.Name) < 3 {
			t.Errorf("name not trimmed: %q", s.Name)
		}
	}
	if len(c.byKind(KindBuild)) != 0 {
		t.Error("a cached fetch must produce no build activity")
	}
	if s := p.Summary(); s.PlanFetch != 4 || s.PlanBuild != 0 {
		t.Errorf("plan = %+v", s)
	}
}

func TestShortSubstitutionsGetNoSpanButStillEmit(t *testing.T) {
	// Stamp lines 1 microsecond apart: every substitution then lasts far less
	// than the 250 ms threshold, so all are marked NoSpan.
	_, c, _ := feedFileStep(t, "cached-fetch.jsonl", Options{MinSubstituteSpan: 250 * time.Millisecond}, time.Microsecond)
	subs := c.byKind(KindSubstitute)
	if len(subs) != 4 {
		t.Fatalf("every substitution must still be emitted for the metric, got %d", len(subs))
	}
	for _, s := range subs {
		if !s.NoSpan {
			t.Errorf("short substitution kept its span: %+v", s)
		}
	}
	// And a long one keeps its span.
	c2 := &collect{}
	p := NewProcessor(c2, Options{MinSubstituteSpan: 250 * time.Millisecond})
	p.Line([]byte(`{"action":"start","fields":["/nix/store/9m2blbp495aa5rxslryxhjx3hjsnd421-x","https://c.example:8443"],"id":7,"level":0,"parent":0,"text":"","type":108}`), t0)
	p.Line([]byte(`{"action":"stop","id":7}`), t0.Add(time.Second))
	if len(c2.acts) != 1 || c2.acts[0].NoSpan || c2.acts[0].Server != "c.example" {
		t.Errorf("long substitution = %+v", c2.acts)
	}
}

func TestFailedKeepGoingGolden(t *testing.T) {
	p, c, _ := feedFile(t, "failed-build-keep-going.jsonl", Options{})
	// Errors arrive late; the failed builds are emitted when named.
	for _, name := range []string{"kq2-fail1-1790833937", "kq2-fail2-1790833937"} {
		a, ok := c.named(name)
		if !ok || !a.Failed || a.ErrorType != ErrBuildFailed || a.Instant {
			t.Errorf("%s = %+v (found %v)", name, a, ok)
		}
		if !strings.Contains(a.Reason, "builder failed with exit code") {
			t.Errorf("%s reason = %q", name, a.Reason)
		}
	}
	p.Finish(t0.Add(time.Hour))
	for _, name := range []string{"kq2-ok-1790833937", "kq2-slow-1790833937"} {
		a, ok := c.named(name)
		if !ok || a.Failed {
			t.Errorf("%s = %+v (found %v)", name, a, ok)
		}
	}
	if got := len(c.byKind(KindBuild)); got != 4 {
		t.Errorf("want 4 builds, got %d", got)
	}
	if s := p.Summary(); s.PlanBuild != 4 {
		t.Errorf("plan = %+v", s)
	}
}

func TestFailedDependencyGolden(t *testing.T) {
	p, c, _ := feedFile(t, "failed-build-dependency.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	fail, ok := c.named("kq2-fail1-1790833959")
	if !ok || !fail.Failed || fail.ErrorType != ErrBuildFailed || fail.Instant {
		t.Errorf("primary failure = %+v", fail)
	}
	dep, ok := c.named("kq2-dep-1790833959")
	if !ok {
		t.Fatal("the dependent drv must be attributed from the message")
	}
	if !dep.Failed || dep.ErrorType != ErrDependencyFailed || !dep.Instant || !dep.Start.Equal(dep.End) {
		t.Errorf("dependent = %+v", dep)
	}
	if ok, found := c.named("kq2-ok-1790833959"); !found || ok.Failed {
		t.Errorf("ok build = %+v", ok)
	}
}

func TestWaitForLockGolden(t *testing.T) {
	p, c, _ := feedFile(t, "wait-for-lock.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	w := c.byKind(KindWait)
	if len(w) != 1 || w[0].Name != "kq2-longfix-52121705" || w[0].Failed {
		t.Fatalf("waits = %+v", w)
	}
	if strings.Contains(w[0].Path, "\x1b") || !strings.HasPrefix(w[0].Path, "/nix/store/") {
		t.Errorf("wait path not cleaned of ANSI: %q", w[0].Path)
	}
}

func TestKilledMidBuildClosesOpenActivitiesWithError(t *testing.T) {
	p, c, n := feedFile(t, "killed-mid-build.jsonl", Options{})
	if len(c.acts) != 0 {
		t.Fatalf("nothing is finished before Finish, got %+v", c.acts)
	}
	end := t0.Add(time.Duration(n+5) * time.Millisecond)
	p.Finish(end)
	if len(c.acts) != 1 {
		t.Fatalf("want the one open build closed, got %+v", c.acts)
	}
	a := c.acts[0]
	if a.Kind != KindBuild || !a.Failed || a.ErrorType != ErrAborted || !a.End.Equal(end) {
		t.Errorf("aborted build = %+v", a)
	}
	if a.Reason == "" {
		t.Error("an aborted activity must say why")
	}
}

func TestFlakeCheckGolden(t *testing.T) {
	p, c, _ := feedFile(t, "flake-check.jsonl", Options{})
	p.Finish(t0.Add(time.Hour))
	if got := len(c.byKind(KindBuild)); got != 2 {
		t.Errorf("flake check builds = %d, want 2", got)
	}
	for _, a := range c.acts {
		if a.Failed {
			t.Errorf("unexpected failure %+v", a)
		}
	}
}

func TestStopBeforeFailureMessageIsHeldThenFailed(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	drv := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-thing.drv"
	p.Line([]byte(fmt.Sprintf(`{"action":"start","fields":[%q,"",1,1],"id":1,"level":3,"parent":0,"text":"building","type":105}`, drv)), t0)
	p.Line([]byte(`{"action":"stop","id":1}`), t0.Add(time.Second))
	if len(c.acts) != 0 {
		t.Fatal("a stopped build must wait for a possible failure message")
	}
	msg := fmt.Sprintf(`{"action":"msg","level":0,"msg":"error: Cannot build '\u001b[35;1m%s\u001b[0m'.\n Reason: \u001b[31;1mbuilder failed with exit code 2\u001b[0m.\n"}`, drv)
	p.Line([]byte(msg), t0.Add(time.Minute))
	if len(c.acts) != 1 || !c.acts[0].Failed || c.acts[0].End != t0.Add(time.Second) {
		t.Fatalf("late failure: %+v", c.acts)
	}
	p.Line([]byte(msg), t0.Add(2*time.Minute)) // duplicate message
	p.Finish(t0.Add(time.Hour))
	if len(c.acts) != 1 {
		t.Errorf("a repeated message must not emit twice: %+v", c.acts)
	}
}

func TestFailureMessageBeforeStop(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	drv := "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-early.drv"
	p.Line([]byte(fmt.Sprintf(`{"action":"start","fields":[%q],"id":2,"level":3,"parent":0,"text":"","type":105}`, drv)), t0)
	p.Line([]byte(fmt.Sprintf(`{"action":"msg","level":0,"msg":"Cannot build '%s'.\nReason: builder failed with exit code 1."}`, drv)), t0.Add(time.Second))
	if len(c.acts) != 0 {
		t.Fatalf("an open build is emitted at its stop, got %+v", c.acts)
	}
	p.Line([]byte(`{"action":"stop","id":2}`), t0.Add(2*time.Second))
	if len(c.acts) != 1 || !c.acts[0].Failed || c.acts[0].Instant {
		t.Fatalf("got %+v", c.acts)
	}
}

func TestRawMsgPreferredAndNonErrorLevelIgnored(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	drv := "/nix/store/cccccccccccccccccccccccccccccccc-raw.drv"
	// A level-3 message that mentions Cannot build is not a failure signal.
	p.Line([]byte(fmt.Sprintf(`{"action":"msg","level":3,"msg":"Cannot build '%s'. Reason: x."}`, drv)), t0)
	if len(c.acts) != 0 {
		t.Fatalf("level 3 must be ignored: %+v", c.acts)
	}
	// raw_msg wins over msg when present.
	p.Line([]byte(fmt.Sprintf(`{"action":"msg","level":0,"msg":"unrelated text","raw_msg":"Cannot build '%s'.\nReason: 1 dependency failed."}`, drv)), t0)
	if len(c.acts) != 1 || c.acts[0].ErrorType != ErrDependencyFailed {
		t.Fatalf("got %+v", c.acts)
	}
}

func TestMalformedAndEmptyLinesAreCountedNotFatal(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	for _, l := range []string{`not json`, `{"action":`, `{}`, `{"action":"stop","id":999}`, `{"action":"result","id":1,"type":106,"fields":[1,2]}`} {
		p.Line([]byte(l), t0)
	}
	p.Finish(t0)
	if s := p.Summary(); s.Malformed != 2 || s.Lines != 5 {
		t.Errorf("summary = %+v", s)
	}
	if len(c.acts) != 0 {
		t.Errorf("garbage produced activities: %+v", c.acts)
	}
}

func TestPlanCountsSum(t *testing.T) {
	c := &collect{}
	p := NewProcessor(c, Options{})
	for _, m := range []string{
		"this derivation will be built:", "these 3 derivations will be built:",
		"this path will be fetched (1 MiB download, 2 MiB unpacked):", "these 300 paths will be fetched (1 MiB download):",
		"  /nix/store/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx-not-a-plan",
	} {
		p.Line([]byte(fmt.Sprintf(`{"action":"msg","level":3,"msg":%q}`, m)), t0)
	}
	if s := p.Summary(); s.PlanBuild != 4 || s.PlanFetch != 301 {
		t.Errorf("plan = %+v", s)
	}
}

func TestStorePathNameAndHost(t *testing.T) {
	cases := map[string]string{
		"/nix/store/9m2blbp495aa5rxslryxhjx3hjsnd421-ffmpeg-8.1.2-bin":         "ffmpeg-8.1.2-bin",
		"/nix/store/5yvsmyrgxi2pp0y7xjj3v91pg3drxl9v-kq2-fail1-1790833959.drv": "kq2-fail1-1790833959",
		"": "", "short": "short",
	}
	for in, want := range cases {
		if got := StorePathName(in); got != want {
			t.Errorf("StorePathName(%q) = %q, want %q", in, got, want)
		}
	}
	if Host("https://cache.nixos.org") != "cache.nixos.org" || Host("ssh://u@h:22") != "h" || Host("weird") != "weird" || Host("") != "" {
		t.Error("Host mishandled an input")
	}
}

func TestManyActivitiesAreAllEmitted(t *testing.T) {
	// 600 substitutions: nothing may be lost in the state machine itself
	// (queue sizing is the exporter's concern, tested in the app package).
	c := &collect{}
	p := NewProcessor(c, Options{})
	for i := 1; i <= 600; i++ {
		p.Line([]byte(fmt.Sprintf(`{"action":"start","fields":["/nix/store/%032d-p%d","https://cache.nixos.org"],"id":%d,"level":0,"parent":0,"text":"","type":108}`, i, i, i)), t0)
	}
	for i := 1; i <= 600; i++ {
		p.Line([]byte(fmt.Sprintf(`{"action":"stop","id":%d}`, i)), t0.Add(time.Second))
	}
	p.Finish(t0)
	if len(c.acts) != 600 {
		t.Errorf("emitted %d of 600", len(c.acts))
	}
}

func TestRetainDropDropsOnlyProgressResults(t *testing.T) {
	for line, want := range map[string]bool{
		`{"action":"result","fields":[1,2,3,4],"id":7,"type":105}`:    true,  // progress
		`{"action":"result","fields":[104,1,2],"id":7,"type":106}`:    true,  // set-expected
		`{"action":"result","fields":["log text"],"id":7,"type":101}`: false, // build log line: kept in the retained log
		`{"action":"result","fields":["x"],"id":7,"type":104}`:        false, // set-phase
		`{"action":"start","id":7,"level":0,"text":"b","type":105}`:   false, // a build ACTIVITY is type 105 too
		`{"action":"stop","id":7}`:                                    false,
		`{"action":"msg","level":0,"msg":"x"}`:                        false,
		`{"action":"result","type":105,"id":7}`:                       false, // unexpected key order is kept, never lost
		``:                                                            false,
	} {
		if got := RetainDrop([]byte(line)); got != want {
			t.Errorf("RetainDrop(%s) = %v, want %v", line, got, want)
		}
	}
	// The parser still skips 101 unread; only the retained log keeps it.
	p := NewProcessor(&collect{}, Options{})
	p.Line([]byte(`{"action":"result","fields":["x"],"id":7,"type":101}`), t0)
	if s := p.Summary(); s.Dropped != 1 || s.Folded != 0 {
		t.Errorf("101 line not skipped: %+v", s)
	}
}
