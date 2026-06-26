package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// ─── fixtures / helpers ────────────────────────────────────────────────────────

// deepcleanFixture creates a temp HOME and scripts the FakeRunner for a DeepClean
// run. When live is true, it scripts storeSize twice (before + after GC), the
// sudo GC call, and runtimeRootsSummary's print-roots call. When live is false
// (dry-run), it scripts the dead-paths estimate calls.
//
// It does NOT create any profile symlinks, so all processProfile sections skip
// silently (os.Lstat on /nix/var/nix/profiles/system etc. fails). Tests that
// need pruning create their own profiles and script list-generations.
func deepcleanFixture(t *testing.T, live bool) (Env, *exec.FakeRunner) {
	t.Helper()
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	if live {
		// storeSize is called twice: before sections and after GC.
		scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 10.0 GB (10737418240 Bytes)")
		scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 9.0 GB (9663676416 Bytes)")
		f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{}, nil)
		f.AddResponse("nix-store", []string{"--gc", "--print-roots"},
			exec.Result{Stdout: []byte("")}, nil)
	} else {
		// dead-paths estimate: sudo nix-store --gc --print-dead (empty → 0 B)
		f.AddResponse("sudo", []string{"nix-store", "--gc", "--print-dead"},
			exec.Result{Stdout: []byte("")}, nil)
	}

	return Env{Home: home}, f
}

// writeStoreConfig writes a store.toml with the given keep_days / keep_count /
// search_dirs under env.configHome()/pn/store.toml.
func writeStoreConfig(t *testing.T, env Env, keepDays, keepCount int, searchDirs []string) {
	t.Helper()
	dir := filepath.Join(env.configHome(), "pn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	var sb strings.Builder
	sb.WriteString("keep_days = ")
	sb.WriteString(itoa(keepDays))
	sb.WriteString("\nkeep_count = ")
	sb.WriteString(itoa(keepCount))
	sb.WriteString("\n")
	if len(searchDirs) > 0 {
		sb.WriteString("search_dirs = [")
		for i, d := range searchDirs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(`"`)
			sb.WriteString(d)
			sb.WriteString(`"`)
		}
		sb.WriteString("]\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "store.toml"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write store.toml: %v", err)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// makeUserProfile creates a user profile symlink at
// ~/.local/state/nix/profiles/<name> and scripts its list-generations response.
// Returns the profile path.
func makeUserProfile(t *testing.T, env Env, f *exec.FakeRunner, name, genStdout string) string {
	t.Helper()
	dir := filepath.Join(env.Home, ".local/state/nix/profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	profile := filepath.Join(dir, name)
	if err := os.Symlink("/nix/store/fake-profile", profile); err != nil {
		t.Fatalf("symlink profile: %v", err)
	}
	f.AddResponse("nix-env", []string{"--profile", profile, "--list-generations"},
		exec.Result{Stdout: []byte(genStdout)}, nil)
	return profile
}

// daysAgo returns the YYYY-MM-DD date string for n days before now.
func daysAgo(n int) string {
	return time.Now().AddDate(0, 0, -n).Format("2006-01-02")
}

// findCall returns the first recorded call matching name where every token in
// argsContain appears in the call's Args (in any position). Returns nil if none.
func findCall(calls []exec.Call, name string, argsContain ...string) *exec.Call {
	for i := range calls {
		c := calls[i]
		if c.Name != name {
			continue
		}
		all := true
		for _, want := range argsContain {
			found := false
			for _, a := range c.Args {
				if a == want {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return &calls[i]
		}
	}
	return nil
}

// hasDeleteGenerations reports whether any call deleted generations.
func hasDeleteGenerations(calls []exec.Call) bool {
	for _, c := range calls {
		for _, a := range c.Args {
			if a == "--delete-generations" {
				return true
			}
		}
	}
	return false
}

// ─── 1. TestParseKeepSince ─────────────────────────────────────────────────────

func TestParseKeepSince(t *testing.T) {
	cases := []struct {
		in       string
		wantDays int
		wantOK   bool
	}{
		{"7d", 7, true},
		{"2w", 14, true},
		{"0d", 0, true},
		{"0w", 0, true},
		{"garbage", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		days, ok := parseKeepSince(tc.in)
		if ok != tc.wantOK {
			t.Errorf("parseKeepSince(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
		}
		if tc.wantOK && days != tc.wantDays {
			t.Errorf("parseKeepSince(%q) days = %d, want %d", tc.in, days, tc.wantDays)
		}
	}
}

// ─── 2. TestDeepClean_InvalidKeepSince ──────────────────────────────────────────

func TestDeepClean_InvalidKeepSince(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "garbage",
	})
	if err == nil {
		t.Fatal("expected error for invalid --keep-since, got nil")
	}
	if !strings.Contains(err.Error(), "--keep-since") {
		t.Errorf("error must mention --keep-since; got: %v", err)
	}
}

// ─── 3. TestDeepClean_EmptyKeepSinceUsesConfigDefault ───────────────────────────

func TestDeepClean_EmptyKeepSinceUsesConfigDefault(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	writeStoreConfig(t, env, 14, 0, nil) // keep_days=14, keep_count=0
	// A user profile with a single generation dated 10d ago — protected by the
	// 14d config window. Empty KeepSince must NOT be treated as 0d.
	makeUserProfile(t, env, f, "fake-user",
		"   1   "+daysAgo(10)+" 12:00:00 (current)\n")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	if hasDeleteGenerations(f.Calls()) {
		t.Errorf("empty KeepSince must use 14d config (gen protected); got a --delete-generations call.\n%s", buf.String())
	}
}

// ─── 4. TestDeepClean_DryRunNoDeletesNoGC ───────────────────────────────────────

func TestDeepClean_DryRunNoDeletesNoGC(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "0d", Keep: 0,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	calls := f.Calls()
	if findCall(calls, "sudo", "nix-store", "--gc") != nil && findCall(calls, "sudo", "--print-dead") == nil {
		// the only sudo nix-store --gc allowed in dry-run is the --print-dead variant
		for _, c := range calls {
			if c.Name == "sudo" && len(c.Args) == 2 && c.Args[0] == "nix-store" && c.Args[1] == "--gc" {
				t.Errorf("dry-run must not call `sudo nix-store --gc`")
			}
		}
	}
	if hasDeleteGenerations(calls) {
		t.Error("dry-run must not call --delete-generations")
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("dry-run output must contain DRY RUN; got:\n%s", buf.String())
	}
}

// ─── 5. TestDeepClean_LiveRunsSudoGC ────────────────────────────────────────────

func TestDeepClean_LiveRunsSudoGC(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: false, KeepSince: "0d", Keep: 0,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	var gcCall *exec.Call
	for i := range f.Calls() {
		c := f.Calls()[i]
		if c.Name == "sudo" && len(c.Args) == 2 && c.Args[0] == "nix-store" && c.Args[1] == "--gc" {
			gcCall = &f.Calls()[i]
		}
	}
	if gcCall == nil {
		t.Fatal("live run must call `sudo nix-store --gc`")
	}
	if gcCall.Opts.Stdout == nil {
		t.Error("GC call must stream (Opts.Stdout != nil)")
	}
}

// ─── 6. TestDeepClean_RemovesResultSymlinks ─────────────────────────────────────

func TestDeepClean_RemovesResultSymlinks(t *testing.T) {
	run := func(t *testing.T, dryRun bool) (string, bool) {
		t.Helper()
		env, f := deepcleanFixture(t, !dryRun)
		searchDir := filepath.Join(env.Home, "projects")
		if err := os.MkdirAll(searchDir, 0o755); err != nil {
			t.Fatalf("mkdir searchdir: %v", err)
		}
		result := filepath.Join(searchDir, "result")
		if err := os.Symlink("/nix/store/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx-out", result); err != nil {
			t.Fatalf("symlink result: %v", err)
		}
		writeStoreConfig(t, env, 14, 3, []string{searchDir})

		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: dryRun, KeepSince: "0d", Keep: -1,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		_, lstatErr := os.Lstat(result)
		return buf.String(), lstatErr == nil // returns (output, stillExists)
	}

	t.Run("live removes", func(t *testing.T) {
		out, stillExists := run(t, false)
		if stillExists {
			t.Errorf("live run must remove result symlink; output:\n%s", out)
		}
		if !strings.Contains(out, "result symlink(s) to remove") {
			t.Errorf("output must list result symlinks; got:\n%s", out)
		}
	})
	t.Run("dry-run preserves", func(t *testing.T) {
		out, stillExists := run(t, true)
		if !stillExists {
			t.Errorf("dry-run must NOT remove result symlink; output:\n%s", out)
		}
	})
}

// ─── 7. TestDeepClean_RemovesOrphanedStandaloneHM ───────────────────────────────

func setupOrphanedHM(t *testing.T, home string) (hmProfile, genLink string) {
	t.Helper()
	profilesDir := filepath.Join(home, ".local/state/nix/profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	hmProfile = filepath.Join(profilesDir, "home-manager")
	genLink = filepath.Join(profilesDir, "home-manager-195-link")
	if err := os.Symlink("/nix/store/standalone", hmProfile); err != nil {
		t.Fatalf("symlink hm: %v", err)
	}
	if err := os.Symlink("/nix/store/standalone", genLink); err != nil {
		t.Fatalf("symlink genlink: %v", err)
	}
	gcrootsDir := filepath.Join(home, ".local/state/home-manager/gcroots")
	if err := os.MkdirAll(gcrootsDir, 0o755); err != nil {
		t.Fatalf("mkdir gcroots: %v", err)
	}
	if err := os.Symlink("/nix/store/darwin", filepath.Join(gcrootsDir, "current-home")); err != nil {
		t.Fatalf("symlink current-home: %v", err)
	}
	return hmProfile, genLink
}

func TestDeepClean_RemovesOrphanedStandaloneHM(t *testing.T) {
	t.Run("live removes", func(t *testing.T) {
		env, f := deepcleanFixture(t, true)
		hmProfile, genLink := setupOrphanedHM(t, env.Home)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: false, KeepSince: "0d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if !strings.Contains(buf.String(), "orphaned standalone profile") {
			t.Errorf("output must mention orphaned standalone profile; got:\n%s", buf.String())
		}
		if _, err := os.Lstat(hmProfile); err == nil {
			t.Error("live run must remove home-manager profile symlink")
		}
		if _, err := os.Lstat(genLink); err == nil {
			t.Error("live run must remove home-manager-195-link")
		}
	})
	t.Run("dry-run preserves", func(t *testing.T) {
		env, f := deepcleanFixture(t, false)
		hmProfile, genLink := setupOrphanedHM(t, env.Home)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: true, KeepSince: "0d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if !strings.Contains(buf.String(), "orphaned standalone profile") {
			t.Errorf("dry-run output must still mention orphaned standalone profile; got:\n%s", buf.String())
		}
		if _, err := os.Lstat(hmProfile); err != nil {
			t.Error("dry-run must NOT remove home-manager profile symlink")
		}
		if _, err := os.Lstat(genLink); err != nil {
			t.Error("dry-run must NOT remove home-manager-195-link")
		}
	})
}

// ─── 8. TestDeepClean_RemovesStaleNixProfiles ───────────────────────────────────

func TestDeepClean_RemovesStaleNixProfiles(t *testing.T) {
	setup := func(t *testing.T, env Env) string {
		t.Helper()
		dir := filepath.Join(env.Home, ".nix-profiles")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir nix-profiles: %v", err)
		}
		old := filepath.Join(dir, "old-profile")
		if err := os.Symlink("/nix/store/old", old); err != nil {
			t.Fatalf("symlink old: %v", err)
		}
		// inject a 30d-old mtime via the lstatModTime seam
		orig := lstatModTime
		stale := time.Now().AddDate(0, 0, -30)
		lstatModTime = func(string) (time.Time, error) { return stale, nil }
		t.Cleanup(func() { lstatModTime = orig })
		return old
	}

	t.Run("live removes", func(t *testing.T) {
		env, f := deepcleanFixture(t, true)
		old := setup(t, env)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: false, KeepSince: "14d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if _, err := os.Lstat(old); err == nil {
			t.Errorf("live run must remove stale profile; output:\n%s", buf.String())
		}
		if !strings.Contains(buf.String(), "stale profile(s) to remove") {
			t.Errorf("output must list stale profiles; got:\n%s", buf.String())
		}
	})
	t.Run("dry-run preserves", func(t *testing.T) {
		env, f := deepcleanFixture(t, false)
		old := setup(t, env)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: true, KeepSince: "14d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if _, err := os.Lstat(old); err != nil {
			t.Error("dry-run must NOT remove stale profile")
		}
	})
}

// ─── 9. TestDeepClean_RemovesNHTempRoots ────────────────────────────────────────

func TestDeepClean_RemovesNHTempRoots(t *testing.T) {
	setup := func(t *testing.T, env *Env) string {
		t.Helper()
		tmp := t.TempDir()
		env.TMPDIR = tmp
		nhDir := filepath.Join(tmp, "nh-darwinABCDEF")
		if err := os.MkdirAll(nhDir, 0o755); err != nil {
			t.Fatalf("mkdir nh: %v", err)
		}
		root := filepath.Join(nhDir, "result")
		if err := os.Symlink("/nix/store/nh-out", root); err != nil {
			t.Fatalf("symlink nh root: %v", err)
		}
		return root
	}

	t.Run("live removes", func(t *testing.T) {
		env, f := deepcleanFixture(t, true)
		root := setup(t, &env)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: false, KeepSince: "0d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if _, err := os.Lstat(root); err == nil {
			t.Errorf("live run must remove nh temp root; output:\n%s", buf.String())
		}
		if !strings.Contains(buf.String(), "temp root(s) to remove") {
			t.Errorf("output must list temp roots; got:\n%s", buf.String())
		}
	})
	t.Run("dry-run preserves", func(t *testing.T) {
		env, f := deepcleanFixture(t, false)
		root := setup(t, &env)
		var buf, errBuf bytes.Buffer
		s := NewWithEnv(f, env)
		if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
			DryRun: true, KeepSince: "0d", Keep: 0,
		}); err != nil {
			t.Fatalf("DeepClean: %v", err)
		}
		if _, err := os.Lstat(root); err != nil {
			t.Error("dry-run must NOT remove nh temp root")
		}
	})
}

// ─── 10. TestDeepClean_NormalHMWhenNotOrphaned ──────────────────────────────────

func TestDeepClean_NormalHMWhenNotOrphaned(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	// HM profile symlink exists but NO current-home gcroot → not orphaned.
	profilesDir := filepath.Join(env.Home, ".local/state/nix/profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	hmProfile := filepath.Join(profilesDir, "home-manager")
	if err := os.Symlink("/nix/store/standalone", hmProfile); err != nil {
		t.Fatalf("symlink hm: %v", err)
	}
	// HM profile is treated as a normal profile → script its list-generations.
	f.AddResponse("nix-env", []string{"--profile", hmProfile, "--list-generations"},
		exec.Result{Stdout: []byte("   1   " + daysAgo(1) + " 12:00:00 (current)\n")}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "0d", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	if strings.Contains(buf.String(), "orphaned standalone profile") {
		t.Errorf("non-orphaned HM must NOT print orphaned message; got:\n%s", buf.String())
	}
}

// ─── 11. TestDeepClean_KeepSinceOverridesConfig ─────────────────────────────────

func TestDeepClean_KeepSinceOverridesConfig(t *testing.T) {
	env, f := deepcleanFixture(t, true)  // live: --delete-generations actually fires
	writeStoreConfig(t, env, 14, 1, nil) // keep_days=14, keep_count=1
	// Two generations: gen1 50d ago, gen2 current. KeepSince=7d overrides 14d.
	// keep_count from config=1 protects the current; gen1 (50d) is past 7d → pruned.
	profile := makeUserProfile(t, env, f, "fake-user",
		"   1   "+daysAgo(50)+" 12:00:00\n   2   "+daysAgo(1)+" 12:00:00 (current)\n")
	f.AddResponse("nix-env", []string{"--profile", profile, "--delete-generations", "1"},
		exec.Result{}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: false, KeepSince: "7d", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	c := findCall(f.Calls(), "nix-env", "--delete-generations", "1", profile)
	if c == nil {
		t.Errorf("expected --delete-generations 1 for %s; calls:\n%v", profile, f.Calls())
	}
}

// ─── 12. TestDeepClean_ConfigDefaultsProtectByCount ─────────────────────────────

func TestDeepClean_ConfigDefaultsProtectByCount(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	writeStoreConfig(t, env, 14, 3, nil) // keep_count=3
	// 2 generations total, both old; keep_count=3 protects both.
	makeUserProfile(t, env, f, "fake-user",
		"   1   "+daysAgo(100)+" 12:00:00\n   2   "+daysAgo(90)+" 12:00:00 (current)\n")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	if hasDeleteGenerations(f.Calls()) {
		t.Error("keep_count=3 must protect both generations; no --delete-generations expected")
	}
	if !strings.Contains(buf.String(), "nothing to prune") {
		t.Errorf("output must contain 'nothing to prune'; got:\n%s", buf.String())
	}
}

// ─── 13. TestDeepClean_ReadsKeepDaysFromConfig ──────────────────────────────────

func TestDeepClean_ReadsKeepDaysFromConfig(t *testing.T) {
	env, f := deepcleanFixture(t, true) // live: --delete-generations actually fires
	writeStoreConfig(t, env, 7, 1, nil) // keep_days=7, keep_count=1
	// gen1 50d ago → past 7d window → pruned; gen2 current → protected.
	profile := makeUserProfile(t, env, f, "fake-user",
		"   1   "+daysAgo(50)+" 12:00:00\n   2   "+daysAgo(1)+" 12:00:00 (current)\n")
	f.AddResponse("nix-env", []string{"--profile", profile, "--delete-generations", "1"},
		exec.Result{}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: false, KeepSince: "", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	c := findCall(f.Calls(), "nix-env", "--delete-generations", "1", profile)
	if c == nil {
		t.Errorf("expected --delete-generations 1 for %s; calls:\n%v", profile, f.Calls())
	}
}

// ─── 14. TestDeepClean_DevboxProjectLabel ───────────────────────────────────────

func TestDeepClean_DevboxProjectLabel(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	projDir := filepath.Join(env.Home, "projects/repo-alpha")
	profileDir := filepath.Join(projDir, ".devbox/nix/profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir devbox profile: %v", err)
	}
	profile := filepath.Join(profileDir, "default")
	if err := os.Symlink("/nix/store/devbox-out", profile); err != nil {
		t.Fatalf("symlink devbox profile: %v", err)
	}
	searchDir := filepath.Join(env.Home, "projects")
	writeStoreConfig(t, env, 14, 3, []string{searchDir})
	// Script the project profile's list-generations (no git → no worktree calls).
	f.AddResponse("nix-env", []string{"--profile", profile, "--list-generations"},
		exec.Result{Stdout: []byte("   1   " + daysAgo(1) + " 12:00:00 (current)\n")}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "0d", Keep: -1,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "~/projects/repo-alpha:") {
		t.Errorf("output must label devbox project as ~/projects/repo-alpha:; got:\n%s", out)
	}
	if regexp.MustCompile(`(?m)^\s+\.devbox:`).MatchString(out) {
		t.Errorf("output must NOT label as .devbox: (label regression); got:\n%s", out)
	}
}

// ─── 15. TestDeepClean_RuntimeRootsAfterGC ──────────────────────────────────────

func TestDeepClean_RuntimeRootsAfterGC(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()
	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 10.0 GB (10737418240 Bytes)")
	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 9.0 GB (9663676416 Bytes)")
	f.AddResponse("sudo", []string{"nix-store", "--gc"}, exec.Result{}, nil)
	// runtimeRootsSummary: an lsof-only path → non-empty summary.
	lsofPath := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-app"
	f.AddResponse("nix-store", []string{"--gc", "--print-roots"},
		exec.Result{Stdout: []byte("/proc/123/maps -> " + lsofPath + " {lsof}\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", lsofPath},
		exec.Result{Stdout: []byte(lsofPath + " 1048576\n")}, nil)

	env := Env{Home: home}
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: false, KeepSince: "0d", Keep: 0,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "=== Runtime Roots ===") {
		t.Fatalf("output must contain Runtime Roots header; got:\n%s", out)
	}
	idxAfter := strings.Index(out, "Store after:")
	idxRoots := strings.Index(out, "=== Runtime Roots ===")
	if idxAfter < 0 || idxRoots < idxAfter {
		t.Errorf("Runtime Roots must come after Store after:; got:\n%s", out)
	}
	if !strings.Contains(out, "held only by running processes") {
		t.Errorf("output must include runtime roots summary content; got:\n%s", out)
	}
}

// ─── 16. TestDeepClean_GoldenDryRunSummary ──────────────────────────────────────

func TestDeepClean_GoldenDryRunSummary(t *testing.T) {
	env, f := deepcleanFixture(t, false)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: true, KeepSince: "0d", Keep: 0,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	out := buf.String()

	// Lock the entire Summary block (em-dash U+2014, 9-category order,
	// generation(s) literal, blank-line placement).
	wantSummary := "" +
		"=== Summary ===\n" +
		"DRY RUN — no changes made\n" +
		"\n" +
		"Would prune:\n" +
		"  system: 0 generation(s)\n" +
		"  home-manager: 0 generation(s)\n" +
		"  user-profiles: 0 generation(s)\n" +
		"  devbox-global: 0 generation(s)\n" +
		"  devbox-util: 0 generation(s)\n" +
		"  devbox-projects: 0 generation(s)\n" +
		"  result-symlinks: 0 generation(s)\n" +
		"  stale-nix-profiles: 0 generation(s)\n" +
		"  nh-temp-roots: 0 generation(s)\n" +
		"\n" +
		"Reclaimable estimate (dead paths):\n" +
		"0 B\n"

	idx := strings.Index(out, "=== Summary ===")
	if idx < 0 {
		t.Fatalf("no Summary section in output:\n%s", out)
	}
	gotSummary := out[idx:]
	if gotSummary != wantSummary {
		t.Errorf("summary mismatch.\nWANT:\n%q\n\nGOT:\n%q", wantSummary, gotSummary)
	}
}
