package logdir

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	f, path, err := Create(dir, now, 4242)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !filepath.IsAbs(path) {
		t.Errorf("path not absolute: %s", path)
	}
	if !regexp.MustCompile(`^20261001T123045Z-4242-[0-9a-f]{8}\.jsonl$`).MatchString(filepath.Base(path)) {
		t.Errorf("bad name %s", filepath.Base(path))
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	// Concurrent wrappers (same second, even same pid) get distinct files.
	seen := map[string]bool{path: true}
	for range 50 {
		g, p, err := Create(dir, now, 4242)
		if err != nil {
			t.Fatal(err)
		}
		g.Close()
		if seen[p] {
			t.Fatalf("duplicate file name %s", p)
		}
		seen[p] = true
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
	if _, _, err := Create(filepath.Join(dir, "missing"), time.Now(), 1); err == nil || errors.Is(err, os.ErrExist) {
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

	if n := Sweep(own, now, 7*24*time.Hour, 0); n != 1 {
		t.Errorf("removed %d, want exactly the one real log", n)
	}
	if exists(mine) {
		t.Error("own stale log survived")
	}
	for name, p := range map[string]string{"other dir": outside, "non-jsonl": notLog, "subdir": nested, "symlink target": victim, "symlink": link} {
		if !exists(p) {
			t.Errorf("%s was deleted", name)
		}
	}
	if Sweep(filepath.Join(root, "missing"), now, time.Hour, 1) != 0 {
		t.Error("missing dir must be a no-op")
	}
}
