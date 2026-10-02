package nixlog

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
	defer f.Close()
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

func TestProgressAndBuildLogResultsDropped(t *testing.T) {
	for _, name := range []string{"cached-fetch.jsonl", "chatty-build.trimmed.jsonl", "local-build.jsonl"} {
		raw, _ := os.ReadFile(filepath.Join("testdata", name))
		want := 0
		for _, l := range bytes.Split(raw, []byte("\n")) {
			if FastDrop(l) {
				want++
			}
		}
		p, _, n := feedFile(t, name, Options{})
		s := p.Summary()
		if s.Dropped != want || want == 0 {
			t.Errorf("%s: dropped %d, want %d (>0)", name, s.Dropped, want)
		}
		if s.Lines != n || s.Malformed != 0 {
			t.Errorf("%s: lines=%d malformed=%d", name, s.Lines, s.Malformed)
		}
	}
}

func TestFastDropIsConservative(t *testing.T) {
	cases := []struct {
		line string
		drop bool
	}{
		{`{"action":"result","fields":[0,0,0,0],"id":1,"type":105}`, true},
		{`{"action":"result","fields":["x"],"id":1,"type":101}`, true},
		{`{"action":"result","fields":[1,2],"id":1,"type":106}`, false},
		{`{"action":"start","id":1,"level":3,"parent":0,"text":"t","type":105}`, false},
		{`{"action":"start","id":1,"type":101}`, false},
		{`{"action":"msg","msg":"type":105}`, false},
	}
	for _, c := range cases {
		if got := FastDrop([]byte(c.line)); got != c.drop {
			t.Errorf("FastDrop(%s) = %v", c.line, got)
		}
	}
	// Key order does not matter for correctness: a reordered 105 result is not
	// fast-dropped but must still produce no activity.
	c := &collect{}
	p := NewProcessor(c, Options{})
	p.Line([]byte(`{"type":105,"id":9,"action":"result","fields":[0,0,0,0]}`), t0)
	p.Finish(t0)
	if len(c.acts) != 0 || p.Summary().Dropped != 0 {
		t.Errorf("reordered result mishandled: %+v %+v", c.acts, p.Summary())
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
