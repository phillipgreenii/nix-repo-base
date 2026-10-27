package logdir

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSelectNonRoot(t *testing.T) {
	cases := []struct {
		name string
		p    Params
		want string
	}{
		{"flag wins", Params{Euid: 501, Flag: "/flag", XDGState: "/xdg", Home: "/h"}, "/flag"},
		{"xdg", Params{Euid: 501, XDGState: "/xdg", Home: "/h"}, "/xdg/pn/nix-logs"},
		{"relative xdg ignored", Params{Euid: 501, XDGState: "rel", Home: "/h"}, "/h/.local/state/pn/nix-logs"},
		{"home fallback", Params{Euid: 501, Home: "/h"}, "/h/.local/state/pn/nix-logs"},
	}
	for _, c := range cases {
		got, err := Select(c.p)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	if _, err := Select(Params{Euid: 501}); err == nil {
		t.Error("no inputs must be an error")
	}
}

func TestSelectRootIsAlwaysTheRootOwnedDirectory(t *testing.T) {
	// Mocked euid 0: neither the flag, nor XDG, nor HOME (kept by sudo's
	// env_keep!) may redirect a root wrapper into a user directory.
	got, err := Select(Params{Euid: 0, Flag: "/Users/u/evil", XDGState: "/Users/u/.state", Home: "/Users/u"})
	if err != nil || got != "/var/log/pg-nix-log-wrapped" {
		t.Errorf("root selected %q, %v", got, err)
	}
	got, _ = Select(Params{Euid: 0, RootDir: "/tmp/test-root"})
	if got != "/tmp/test-root" {
		t.Errorf("test override ignored: %q", got)
	}
}

func TestPrepareRootStrictness(t *testing.T) {
	euid := os.Geteuid()
	base := t.TempDir()
	// We are not root, so exercise the root-mode checks with strict=euid of
	// the test process by calling Prepare(dir, 0) only for the rejection paths
	// that fire before ownership: symlink and permissions.
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(link, 0); err == nil {
		t.Error("root mode must reject a symlinked directory")
	}
	if euid != 0 {
		// Owned by us, not by uid 0: root mode must refuse it.
		if err := Prepare(real, 0); err == nil {
			t.Error("root mode must reject a directory not owned by root")
		}
	}
	// Non-root mode creates the directory with 0700.
	user := filepath.Join(base, "a", "b")
	if err := Prepare(user, euid+1); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(user); fi.Mode().Perm() != 0o700 {
		t.Errorf("user dir mode = %v", fi.Mode().Perm())
	}
}

func TestCreateNamesModeAndExclusivity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 30, 45, 0, time.UTC)
	f, err := Create(dir, now, 4242)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range []string{f.RawPath, f.OutPath} {
		if !filepath.IsAbs(p) {
			t.Errorf("path not absolute: %s", p)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v", p, fi.Mode().Perm())
		}
	}
	if !regexp.MustCompile(`^20261001T123045Z-4242-[0-9a-f]{8}\.raw$`).MatchString(filepath.Base(f.RawPath)) {
		t.Errorf("bad scratch name %s", filepath.Base(f.RawPath))
	}
	if !regexp.MustCompile(`^20261001T123045Z-4242-[0-9a-f]{8}\.jsonl$`).MatchString(filepath.Base(f.OutPath)) {
		t.Errorf("bad retained name %s", filepath.Base(f.OutPath))
	}
	if strings.TrimSuffix(f.RawPath, ".raw") != strings.TrimSuffix(f.OutPath, ".jsonl") {
		t.Errorf("the pair must share one stem: %s vs %s", f.RawPath, f.OutPath)
	}
	// Concurrent wrappers (same second, even same pid) get distinct files.
	seen := map[string]bool{f.OutPath: true}
	for range 50 {
		g, err := Create(dir, now, 4242)
		if err != nil {
			t.Fatal(err)
		}
		g.Close()
		if seen[g.OutPath] || seen[g.RawPath] {
			t.Fatalf("duplicate file name %s", g.OutPath)
		}
		seen[g.OutPath], seen[g.RawPath] = true, true
	}
}

func TestFilesRemoveDeletesBoth(t *testing.T) {
	f, err := Create(t.TempDir(), time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	f.Remove()
	if exists(f.RawPath) || exists(f.OutPath) {
		t.Error("Remove left a file behind")
	}
}

func TestCreateRefusesSymlinkAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	// A symlink where the file would go is never followed (O_NOFOLLOW|O_EXCL):
	// pre-plant every name Create could pick is infeasible, so test the open
	// flags directly the way Create uses them.
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "x.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := os.OpenFile(link, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		t.Fatal("open through a symlink must fail")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Error("symlink target was modified")
	}
	if _, err := Create(filepath.Join(dir, "missing"), time.Now(), 1); err == nil || errors.Is(err, os.ErrExist) {
		t.Errorf("missing dir must error, got %v", err)
	}
}

func put(t *testing.T, dir, name string, size int, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestSweepAgeThenSizeCapOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	day := 24 * time.Hour
	old := put(t, dir, "old.jsonl", 10, 8*day, now)
	edge := put(t, dir, "edge.jsonl", 10, 6*day, now)
	a := put(t, dir, "a.jsonl", 100, 3*day, now)
	b := put(t, dir, "b.jsonl", 100, 2*day, now)
	c := put(t, dir, "c.jsonl", 100, 1*day, now)
	if n := Sweep(dir, now, 7*day, 1<<30); n != 1 || exists(old) || !exists(edge) {
		t.Fatalf("age phase removed %d; old=%v", n, exists(old))
	}
	// Total is now 310; cap 250 forces oldest-first removal: edge (10), then a.
	if n := Sweep(dir, now, 7*day, 250); n != 2 {
		t.Fatalf("size phase removed %d", n)
	}
	if exists(edge) || exists(a) || !exists(b) || !exists(c) {
		t.Errorf("wrong survivors: edge=%v a=%v b=%v c=%v", exists(edge), exists(a), exists(b), exists(c))
	}
}

func TestSweepOnlyTouchesItsOwnDirectoryAndJSONL(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "own")
	other := filepath.Join(root, "other")
	for _, d := range []string{own, other, filepath.Join(own, "sub")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ancient := 100 * 24 * time.Hour
	outside := put(t, other, "ancient.jsonl", 10, ancient, now)
	notLog := put(t, own, "notes.txt", 10, ancient, now)
	nested := put(t, filepath.Join(own, "sub"), "ancient.jsonl", 10, ancient, now)
	victim := put(t, other, "victim.jsonl", 10, ancient, now)
	link := filepath.Join(own, "link.jsonl")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	mine := put(t, own, "ancient.jsonl", 10, ancient, now)
	// Scratch files: an orphan .raw (dead pid) in ANOTHER directory and a .raw
	// symlink in the sweep's own directory must be left alone too.
	otherRaw := put(t, other, "20260101T000000Z-77-aabbccdd.raw", 10, ancient, now)
	rawLink := filepath.Join(own, "20260101T000000Z-78-aabbccdd.raw")
	if err := os.Symlink(victim, rawLink); err != nil {
		t.Fatal(err)
	}

	if n := SweepWith(own, now, 7*24*time.Hour, 0, SweepOptions{PidAlive: func(int) bool { return false }}); n != 1 {
		t.Errorf("removed %d, want exactly the one real log", n)
	}
	if exists(mine) {
		t.Error("own stale log survived")
	}
	for name, p := range map[string]string{"other dir": outside, "non-jsonl": notLog, "subdir": nested, "symlink target": victim, "symlink": link, "other dir raw": otherRaw, "raw symlink": rawLink} {
		if !exists(p) {
			t.Errorf("%s was deleted", name)
		}
	}
	if Sweep(filepath.Join(root, "missing"), now, time.Hour, 1) != 0 {
		t.Error("missing dir must be a no-op")
	}
}

// rawLines is a small raw nix log: start/stop/msg, a build-log line (101),
// progress (105) and set-expected (106) results.
var rawLines = []string{
	`{"action":"start","id":1,"level":0,"parent":0,"text":"building","type":105}`,
	`{"action":"result","fields":[1,2,3,4],"id":1,"type":105}`,
	`{"action":"result","fields":["compiling"],"id":1,"type":101}`,
	`{"action":"result","fields":[104,1,2],"id":1,"type":106}`,
	`{"action":"msg","level":0,"msg":"error: boom","raw_msg":"boom"}`,
	`{"action":"stop","id":1}`,
}

func writeRaw(t *testing.T, dir, name string, lines []string, partial string, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := strings.Join(lines, "\n") + "\n" + partial
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestSweepFinalizesAnOrphanRawIntoAFilteredJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	raw := writeRaw(t, dir, "20261001T000000Z-4242-aabbccdd.raw", rawLines, `{"action":"sto`, time.Hour, now)
	// The partial retained file the dead wrapper left behind is replaced.
	out := filepath.Join(dir, "20261001T000000Z-4242-aabbccdd.jsonl")
	if err := os.WriteFile(out, []byte("stale partial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var probed []int
	opts := SweepOptions{PidAlive: func(pid int) bool { probed = append(probed, pid); return false }}
	if n := SweepWith(dir, now, 7*24*time.Hour, 1<<30, opts); n != 0 {
		t.Errorf("evicted %d, want 0 (finalizing is not eviction)", n)
	}
	if len(probed) != 1 || probed[0] != 4242 {
		t.Errorf("pid probes = %v, want [4242] parsed from the name", probed)
	}
	if exists(raw) {
		t.Error("finalized .raw must be removed")
	}
	got := readLines(t, out)
	want := []string{rawLines[0], rawLines[2], rawLines[4], rawLines[5]} // 105/106 results and the partial line dropped
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("finalized lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The finalized file carries the run's age, not the sweep's.
	if fi, _ := os.Stat(out); now.Sub(fi.ModTime()) < 59*time.Minute {
		t.Errorf("finalized mtime %v does not preserve the raw file's age", fi.ModTime())
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestSweepFinalizeRespectsTheRetainedCapAndDoesNotDeleteUnfiltered(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	lines := []string{`{"action":"msg","level":0,"msg":"aaaa"}`, `{"action":"msg","level":0,"msg":"bbbb"}`, `{"action":"msg","level":0,"msg":"cccc"}`}
	writeRaw(t, dir, "20261001T000000Z-1-aabbccdd.raw", lines, "", time.Minute, now)
	capBytes := int64(len(lines[0])+1) + int64(len(lines[1])+1) // exactly two lines fit
	SweepWith(dir, now, time.Hour, 1<<30, SweepOptions{PidAlive: func(int) bool { return false }, RetainedCap: capBytes})
	out := filepath.Join(dir, "20261001T000000Z-1-aabbccdd.jsonl")
	got := readLines(t, out)
	if len(got) != 2 || got[0] != lines[0] || got[1] != lines[1] {
		t.Errorf("capped finalize = %v", got)
	}
	if fi, _ := os.Stat(out); fi.Size() > capBytes {
		t.Errorf("finalized file %d bytes exceeds cap %d", fi.Size(), capBytes)
	}
}

func TestSweepLeavesARawWhosePidIsAlive(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	raw := writeRaw(t, dir, "20261001T000000Z-4242-aabbccdd.raw", rawLines, "", time.Hour, now)
	if n := SweepWith(dir, now, 7*24*time.Hour, 1<<30, SweepOptions{PidAlive: func(int) bool { return true }}); n != 0 {
		t.Errorf("evicted %d", n)
	}
	if b, _ := os.ReadFile(raw); !strings.Contains(string(b), `"type":105}`) {
		t.Error("a live run's .raw was modified or removed")
	}
	if exists(strings.TrimSuffix(raw, ".raw") + ".jsonl") {
		t.Error("a live run's .raw was finalized")
	}
}

func TestSweepFinalizesAnOverAgeRawEvenIfItsPidLooksAlive(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	day := 24 * time.Hour
	raw := writeRaw(t, dir, "20260101T000000Z-4242-aabbccdd.raw", rawLines, "", 8*day, now)
	SweepWith(dir, now, 7*day, 1<<30, SweepOptions{PidAlive: func(int) bool { return true }}) // pid reuse
	if exists(raw) {
		t.Error("an over-age .raw must be finalized even when its pid is alive")
	}
	// The finalized file keeps the raw's 8-day-old mtime, so the same sweep's
	// age phase then evicts it like any other expired .jsonl; it is never left
	// behind unfiltered.
	out := strings.TrimSuffix(raw, ".raw") + ".jsonl"
	if exists(out) {
		t.Error("an 8-day-old finalized file must be age-evicted by the same sweep")
	}
}

func TestSweepRawWithoutAPidIsLeftUntilItIsOverAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	young := writeRaw(t, dir, "weird.raw", rawLines, "", time.Hour, now)
	old := writeRaw(t, dir, "weird2.raw", rawLines, "", 10*24*time.Hour, now)
	SweepWith(dir, now, 7*24*time.Hour, 1<<30, SweepOptions{PidAlive: func(int) bool { return false }})
	if !exists(young) {
		t.Error("a .raw with no parseable pid must not be touched while young")
	}
	if exists(old) {
		t.Error("an over-age .raw must be finalized")
	}
}

func TestSweepCountsLiveRawTowardTheCapButNeverEvictsIt(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	day := 24 * time.Hour
	alive := func(int) bool { return true }
	liveRaw := put(t, dir, "20261001T000000Z-4242-aaaaaaaa.raw", 100, time.Hour, now)
	liveOut := put(t, dir, "20261001T000000Z-4242-aaaaaaaa.jsonl", 100, 10*day, now) // old, but its run is live
	a := put(t, dir, "a.jsonl", 100, 3*day, now)
	b := put(t, dir, "b.jsonl", 100, 2*day, now)
	c := put(t, dir, "c.jsonl", 100, 1*day, now)
	// Total is 500 (the live .raw counts). Cap 250 evicts oldest-first among
	// FINALIZED files only: a (400), b (300), c (200) stops at <= 250 after c.
	n := SweepWith(dir, now, 30*day, 250, SweepOptions{PidAlive: alive})
	if n != 3 || exists(a) || exists(b) || exists(c) {
		t.Errorf("evicted %d; a=%v b=%v c=%v", n, exists(a), exists(b), exists(c))
	}
	if !exists(liveRaw) || !exists(liveOut) {
		t.Error("a live run's .raw and .jsonl must never be evicted")
	}
	// Without the live .raw in the total, two files (200 bytes) would fit a
	// 250 cap and only a would go: the raw's 100 bytes forced the extra eviction.
	dir2 := t.TempDir()
	put(t, dir2, "20261001T000000Z-4242-aaaaaaaa.raw", 100, time.Hour, now)
	a2 := put(t, dir2, "a.jsonl", 100, 3*day, now)
	b2 := put(t, dir2, "b.jsonl", 100, 2*day, now)
	if n := SweepWith(dir2, now, 30*day, 250, SweepOptions{PidAlive: alive}); n != 1 || exists(a2) || !exists(b2) {
		t.Errorf("raw not counted: evicted %d a=%v b=%v", n, exists(a2), exists(b2))
	}
}
