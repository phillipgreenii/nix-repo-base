package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ─── userProfiles ─────────────────────────────────────────────────────────────

func TestUserProfiles_ExcludesHMAndGenLinks(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".local/state/nix/profiles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"home-manager", "home-manager-195-link", "channels", "channels-3-link"} {
		if err := os.WriteFile(filepath.Join(pdir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewWithEnv(nil, Env{Home: home})
	got := s.userProfiles()
	if len(got) != 1 || filepath.Base(got[0]) != "channels" {
		t.Fatalf("userProfiles = %v, want [.../channels]", got)
	}
}

func TestUserProfiles_AbsentDirReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	s := NewWithEnv(nil, Env{Home: home})
	got := s.userProfiles()
	if len(got) != 0 {
		t.Fatalf("expected empty from absent profiles dir, got %v", got)
	}
}

// ─── formatProfileLabel ───────────────────────────────────────────────────────

func TestFormatProfileLabel_DevboxProjectTilde(t *testing.T) {
	home := "/Users/me"
	s := NewWithEnv(nil, Env{Home: home})
	p := home + "/projects/repo-alpha/.devbox/nix/profile/default"
	if got := s.formatProfileLabel(p, "devbox-projects"); got != "~/projects/repo-alpha" {
		t.Fatalf("label = %q, want ~/projects/repo-alpha", got)
	}
	if got := s.formatProfileLabel("/nix/var/nix/profiles/system", "system"); got != "system" {
		t.Fatalf("system label = %q", got)
	}
}

func TestFormatProfileLabel_AllCategories(t *testing.T) {
	home := "/Users/tester"
	s := NewWithEnv(nil, Env{Home: home})

	cases := []struct {
		profile  string
		category string
		want     string
	}{
		{"/nix/var/nix/profiles/system", "system", "system"},
		{home + "/.local/state/nix/profiles/home-manager", "home-manager", "home-manager"},
		{home + "/.local/share/devbox/global/default/.devbox/nix/profile/default", "devbox-global", "devbox-global"},
		{home + "/.local/share/devbox/util/.devbox/nix/profile/default", "devbox-util", "devbox-util"},
		{home + "/.local/state/nix/profiles/channels", "user-profiles", "channels"},
		// devbox-projects inside $HOME
		{home + "/projects/myapp/.devbox/nix/profile/default", "devbox-projects", "~/projects/myapp"},
		// devbox-projects outside $HOME
		{"/work/repo/.devbox/nix/profile/default", "devbox-projects", "/work/repo"},
	}

	for _, tc := range cases {
		got := s.formatProfileLabel(tc.profile, tc.category)
		if got != tc.want {
			t.Errorf("formatProfileLabel(%q, %q) = %q, want %q", tc.profile, tc.category, got, tc.want)
		}
	}
}

// ─── staleNixProfiles ─────────────────────────────────────────────────────────

func TestStaleNixProfiles_MtimeThreshold(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".nix-profiles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pdir, "old-env-1-link")
	// Target need not exist; staleNixProfiles only Lstats the link itself.
	if err := os.Symlink("/nix/store/x", link); err != nil {
		t.Fatal(err)
	}
	// Inject the symlink mtime via the seam — do NOT use os.Chtimes (it follows
	// the symlink and would ENOENT on the missing target), and a freshly-created
	// link's real mtime is ~now (would fail the 14d assertion). The seam is the
	// ONLY mtime source in staleNixProfiles, so this fully controls the test.
	old := time.Now().Add(-30 * 24 * time.Hour)
	orig := lstatModTime
	lstatModTime = func(string) (time.Time, error) { return old, nil }
	defer func() { lstatModTime = orig }()

	s := NewWithEnv(nil, Env{Home: home})
	now := time.Now()
	if got := s.staleNixProfiles(14, now); len(got) != 1 {
		t.Fatalf("expected 1 stale at 14d, got %v", got)
	}
	if got := s.staleNixProfiles(60, now); len(got) != 0 {
		t.Fatalf("expected 0 stale at 60d, got %v", got)
	}
	if got := s.staleNixProfiles(0, now); len(got) != 1 {
		t.Fatalf("keepDays=0 should mark all stale, got %v", got)
	}
}

// ─── isOrphanedStandaloneHMProfile ────────────────────────────────────────────

func TestIsOrphanedStandaloneHM(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".local/state/nix/profiles")
	gcroots := filepath.Join(home, ".local/state/home-manager/gcroots")
	os.MkdirAll(pdir, 0o755)
	os.MkdirAll(gcroots, 0o755)
	hm := filepath.Join(pdir, "home-manager")
	os.Symlink("/nix/store/standalone", hm)
	s := NewWithEnv(nil, Env{Home: home})
	if s.isOrphanedStandaloneHMProfile(hm) {
		t.Fatal("not orphaned without current-home")
	}
	os.Symlink("/nix/store/darwin", filepath.Join(gcroots, "current-home"))
	if !s.isOrphanedStandaloneHMProfile(hm) {
		t.Fatal("orphaned when both exist")
	}
}

// ─── discoverResultSymlinks ───────────────────────────────────────────────────

func TestDiscoverResultSymlinks(t *testing.T) {
	root := t.TempDir()

	// result → /nix/store/x should match
	if err := os.Symlink("/nix/store/xhash-result", filepath.Join(root, "result")); err != nil {
		t.Fatal(err)
	}

	// result → /elsewhere should be excluded
	if err := os.Symlink("/elsewhere/thing", filepath.Join(root, "result-bad")); err != nil {
		t.Fatal(err)
	}

	// result-1 → /nix/store/y should match
	if err := os.Symlink("/nix/store/yhash-result", filepath.Join(root, "result-1")); err != nil {
		t.Fatal(err)
	}

	// dangling symlink to /nix/store/... should still match (target doesn't exist on disk)
	if err := os.Symlink("/nix/store/nonexistent-abcdef", filepath.Join(root, "result-dangling")); err != nil {
		t.Fatal(err)
	}

	// Verify dangling target doesn't exist (os.Readlink should still work)
	target, err := os.Readlink(filepath.Join(root, "result-dangling"))
	if err != nil {
		t.Fatalf("os.Readlink on dangling symlink: %v", err)
	}
	if target != "/nix/store/nonexistent-abcdef" {
		t.Fatalf("unexpected target: %q", target)
	}

	got := discoverResultSymlinks([]string{root})

	names := make(map[string]bool)
	for _, p := range got {
		names[filepath.Base(p)] = true
	}

	if !names["result"] {
		t.Error("result → /nix/store/... should match")
	}
	if names["result-bad"] {
		t.Error("result → /elsewhere should be excluded")
	}
	if !names["result-1"] {
		t.Error("result-1 → /nix/store/... should match")
	}
	if !names["result-dangling"] {
		t.Error("dangling result-dangling → /nix/store/... should match")
	}
}

// ─── nhTempRoots ──────────────────────────────────────────────────────────────

func TestNHTempRoots(t *testing.T) {
	tmp := t.TempDir()
	s := NewWithEnv(nil, Env{TMPDIR: tmp})

	// Should match: <tmp>/nh-darwinABCDEF/result
	nhDir := filepath.Join(tmp, "nh-darwinABCDEF")
	if err := os.MkdirAll(nhDir, 0o755); err != nil {
		t.Fatal(err)
	}
	matchLink := filepath.Join(nhDir, "result")
	if err := os.Symlink("/nix/store/nhresult", matchLink); err != nil {
		t.Fatal(err)
	}

	// Should NOT match: <tmp>/result (no nh-darwin parent)
	if err := os.Symlink("/nix/store/x", filepath.Join(tmp, "result")); err != nil {
		t.Fatal(err)
	}

	// Should NOT match: <tmp>/foo/result (parent is foo, not nh-darwin*)
	fooDir := filepath.Join(tmp, "foo")
	if err := os.MkdirAll(fooDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/y", filepath.Join(fooDir, "result")); err != nil {
		t.Fatal(err)
	}

	got := s.nhTempRoots()
	if len(got) != 1 {
		t.Fatalf("nhTempRoots = %v, want exactly 1 match", got)
	}
	if got[0] != matchLink {
		t.Fatalf("nhTempRoots[0] = %q, want %q", got[0], matchLink)
	}
}

// ─── devboxProjects ───────────────────────────────────────────────────────────

func TestDevboxProjects_GitWorktreeExpansion(t *testing.T) {
	root := t.TempDir()

	// Create a "repo" dir with a .git directory
	repoDir := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a worktree dir that has .devbox/nix/profile/default
	wtDir := filepath.Join(root, "myrepo-wt")
	devboxProfile := filepath.Join(wtDir, ".devbox/nix/profile/default")
	if err := os.MkdirAll(filepath.Dir(devboxProfile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devboxProfile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Script the git worktree list --porcelain response
	f := exec.NewFakeRunner()
	porcelainOutput := "worktree " + repoDir + "\nHEAD abc123\nbranch refs/heads/main\n\nworktree " + wtDir + "\nHEAD def456\nbranch refs/heads/feat\n\n"
	f.AddResponse("git", []string{"-C", repoDir, "worktree", "list", "--porcelain"},
		exec.Result{Stdout: []byte(porcelainOutput)}, nil)

	s := NewWithEnv(f, Env{})
	var errBuf bytes.Buffer
	got := s.devboxProjects(context.Background(), &errBuf, []string{root})

	found := false
	for _, p := range got {
		if p == devboxProfile {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("devboxProjects did not find worktree devbox profile %q; got %v", devboxProfile, got)
	}

	// Verify no duplicates
	seen := make(map[string]int)
	for _, p := range got {
		seen[p]++
	}
	for p, count := range seen {
		if count > 1 {
			t.Errorf("duplicate entry %q (count=%d)", p, count)
		}
	}
}

func TestDevboxProjects_MissingSearchDirWarnsToErrOut(t *testing.T) {
	f := exec.NewFakeRunner()
	s := NewWithEnv(f, Env{})
	var errBuf bytes.Buffer
	var stdout bytes.Buffer

	nonExistent := "/nonexistent-dir-" + t.Name()
	got := s.devboxProjects(context.Background(), &errBuf, []string{nonExistent})

	if len(got) != 0 {
		t.Errorf("expected empty result for missing dir, got %v", got)
	}
	warning := errBuf.String()
	if warning == "" {
		t.Error("expected WARNING to errOut for missing search dir, got empty")
	}
	if !bytes.Contains(errBuf.Bytes(), []byte(nonExistent)) {
		t.Errorf("warning %q should mention the missing dir %q", warning, nonExistent)
	}
	// stdout should be unaffected
	_ = stdout
}

// ─── homeManagerGenLinks ──────────────────────────────────────────────────────

func TestHomeManagerGenLinks(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".local/state/nix/profiles")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create: home-manager (not a gen-link), home-manager-195-link, home-manager-7-link, channels-1-link
	names := []string{"home-manager", "home-manager-195-link", "home-manager-7-link", "channels-1-link"}
	for _, n := range names {
		// Create as symlinks (type l)
		target := "/nix/store/fake-" + n
		if err := os.Symlink(target, filepath.Join(pdir, n)); err != nil {
			t.Fatal(err)
		}
	}

	s := NewWithEnv(nil, Env{Home: home})
	got := s.homeManagerGenLinks()

	if len(got) != 2 {
		t.Fatalf("homeManagerGenLinks = %v, want exactly 2 home-manager-*-link entries", got)
	}
	bases := make(map[string]bool)
	for _, p := range got {
		bases[filepath.Base(p)] = true
	}
	if !bases["home-manager-195-link"] {
		t.Error("missing home-manager-195-link")
	}
	if !bases["home-manager-7-link"] {
		t.Error("missing home-manager-7-link")
	}
}

// ─── devboxGlobalProfile / devboxUtilProfile ──────────────────────────────────

func TestDevboxGlobalProfile_ExistsAndAbsent(t *testing.T) {
	home := t.TempDir()
	s := NewWithEnv(nil, Env{Home: home})

	// Absent: should return ""
	if got := s.devboxGlobalProfile(); got != "" {
		t.Fatalf("expected empty when path absent, got %q", got)
	}

	// Create the path
	path := filepath.Join(home, ".local/share/devbox/global/default/.devbox/nix/profile/default")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := s.devboxGlobalProfile(); got != path {
		t.Fatalf("devboxGlobalProfile = %q, want %q", got, path)
	}
}

func TestDevboxUtilProfile_ExistsAndAbsent(t *testing.T) {
	home := t.TempDir()
	s := NewWithEnv(nil, Env{Home: home})

	if got := s.devboxUtilProfile(); got != "" {
		t.Fatalf("expected empty when path absent, got %q", got)
	}

	path := filepath.Join(home, ".local/share/devbox/util/.devbox/nix/profile/default")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := s.devboxUtilProfile(); got != path {
		t.Fatalf("devboxUtilProfile = %q, want %q", got, path)
	}
}
