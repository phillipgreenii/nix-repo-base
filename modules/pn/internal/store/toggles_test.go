package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const (
	devboxNote = "  (disabled; set devbox = true in ~/.config/pn/store.toml to enable)"
	floxNote   = "  (disabled; set flox = true in ~/.config/pn/store.toml to enable)"
)

// ─── config ────────────────────────────────────────────────────────────────────

func TestLoadConfig_DevboxFloxToggles(t *testing.T) {
	cases := []struct {
		name         string
		body         string // "" with write=false means no file
		write        bool
		devbox, flox bool
	}{
		{"file absent", "", false, false, false},
		{"keys absent", "keep_days = 7\n", true, false, false},
		{"malformed toml", "devbox = [[[\n", true, false, false},
		{"both true", "devbox = true\nflox = true\n", true, true, true},
		{"devbox only", "devbox = true\n", true, true, false},
		{"flox only", "flox = true\n", true, false, true},
		{"explicit false", "devbox = false\nflox = false\n", true, false, false},
		{"devbox false flox true", "devbox = false\nflox = true\n", true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
			if tc.write {
				writeStoreTOML(t, env, tc.body)
			}
			c := LoadConfig(env)
			if c.Devbox != tc.devbox || c.Flox != tc.flox {
				t.Fatalf("devbox=%v flox=%v, want devbox=%v flox=%v", c.Devbox, c.Flox, tc.devbox, tc.flox)
			}
		})
	}
}

// A wrong-typed value makes toml.Unmarshal fail, so the WHOLE file is
// discarded and every key reverts to its default (documented behaviour).
func TestLoadConfig_WrongTypedToggleResetsWholeConfig(t *testing.T) {
	env := Env{Home: t.TempDir(), XDGConfigHome: t.TempDir()}
	writeStoreTOML(t, env, "devbox = \"true\"\nflox = true\nkeep_days = 7\nsearch_dirs = [\"/a\"]\n")
	c := LoadConfig(env)
	if c.Devbox || c.Flox || c.KeepDays != 14 || len(c.SearchDirs) != 0 {
		t.Fatalf("config not reset to defaults: %+v", c)
	}
}

func TestCategoryEnabled_AllCategoriesAllCombinations(t *testing.T) {
	devboxCats := map[string]bool{"devbox-global": true, "devbox-util": true, "devbox-projects": true}
	floxCats := map[string]bool{"flox-temp-dirs": true}
	for _, devbox := range []bool{false, true} {
		for _, flox := range []bool{false, true} {
			cfg := Config{Devbox: devbox, Flox: flox}
			for _, cat := range prunedCategories {
				want := true
				switch {
				case devboxCats[cat]:
					want = devbox
				case floxCats[cat]:
					want = flox
				}
				if got := categoryEnabled(cat, cfg); got != want {
					t.Errorf("categoryEnabled(%q, devbox=%v flox=%v) = %v, want %v", cat, devbox, flox, got, want)
				}
			}
		}
	}
}

// ─── deepclean ─────────────────────────────────────────────────────────────────

func runDeepClean(t *testing.T, env Env, f *exec.FakeRunner, dryRun bool, keepSince string) (string, string) {
	t.Helper()
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.DeepClean(context.Background(), &buf, &errBuf, DeepCleanOptions{
		DryRun: dryRun, KeepSince: keepSince, Keep: 0,
	}); err != nil {
		t.Fatalf("DeepClean: %v", err)
	}
	return buf.String(), errBuf.String()
}

func TestDeepClean_DisabledToolsPrintNotesAndRunNoDiscovery(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	searchDir := filepath.Join(env.Home, "projects")
	if err := os.MkdirAll(filepath.Join(searchDir, "repo/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoreConfigToggles(t, env, false, false, searchDir)

	out, _ := runDeepClean(t, env, f, true, "0d")

	for header, note := range map[string]string{
		"=== Devbox Global ===":   devboxNote,
		"=== Devbox Util ===":     devboxNote,
		"=== Devbox Projects ===": devboxNote,
		"=== Flox ===":            floxNote,
		"=== Flox Temp Dirs ===":  floxNote,
	} {
		if !strings.Contains(out, header+"\n"+note+"\n\n") {
			t.Errorf("%s must be followed by note and a blank line; got:\n%s", header, out)
		}
	}
	for _, c := range f.Calls() {
		if c.Name == "git" || c.Name == "du" {
			t.Errorf("unexpected %s call with tools disabled: %v", c.Name, c)
		}
	}
}

func TestDeepClean_DisabledToolsGoldenDryRun(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	out, _ := runDeepClean(t, env, f, true, "0d")
	// The system-profile section depends on the host's real /nix, so compare
	// from the first gated section onward.
	idx := strings.Index(out, "=== Devbox Global ===")
	if idx < 0 {
		t.Fatalf("no Devbox Global section:\n%s", out)
	}
	out = out[idx:]
	want := "" +
		"=== Devbox Global ===\n" + devboxNote + "\n\n" +
		"=== Devbox Util ===\n" + devboxNote + "\n\n" +
		"=== Devbox Projects ===\n" + devboxNote + "\n\n" +
		"=== Flox ===\n" + floxNote + "\n\n" +
		"=== Flox Temp Dirs ===\n" + floxNote + "\n\n" +
		"=== Result Symlinks ===\n  nothing to clean\n\n" +
		"=== Stale Nix Profiles ===\n  nothing to clean\n\n" +
		"=== NH Temp Roots ===\n  nothing to clean\n\n" +
		"=== Summary ===\n" +
		"DRY RUN — no changes made\n" +
		"\n" +
		"Would prune:\n" +
		"  system: 0 generation(s)\n" +
		"  home-manager: 0 generation(s)\n" +
		"  user-profiles: 0 generation(s)\n" +
		"  result-symlinks: 0 generation(s)\n" +
		"  stale-nix-profiles: 0 generation(s)\n" +
		"  nh-temp-roots: 0 generation(s)\n" +
		"\n" +
		"Reclaimable estimate (dead paths):\n" +
		"0 B\n"
	if out != want {
		t.Errorf("both-off dry-run output mismatch.\nWANT:\n%q\n\nGOT:\n%q", want, out)
	}
}

func TestDeepClean_DisabledToolsFilesystemGuards(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	searchDir := filepath.Join(env.Home, "projects")
	mkProfile := func(dir string) string {
		t.Helper()
		pd := filepath.Join(dir, ".devbox/nix/profile")
		if err := os.MkdirAll(pd, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(pd, "default")
		if err := os.Symlink("/nix/store/devbox-out", p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mkProfile(filepath.Join(searchDir, "repo-alpha"))
	mkProfile(filepath.Join(env.Home, ".local/share/devbox/global/default"))
	mkProfile(filepath.Join(env.Home, ".local/share/devbox/util"))
	stale := filepath.Join(env.Home, ".cache/flox/process/.tmpX")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	writeStoreConfigToggles(t, env, false, false, searchDir)

	out, _ := runDeepClean(t, env, f, false, "14d")

	if strings.Contains(out, "repo-alpha") || strings.Contains(out, "devbox-global:") || strings.Contains(out, "devbox-util:") {
		t.Errorf("disabled devbox profiles must not be reported; got:\n%s", out)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale flox temp dir must survive with flox off; stat error: %v", err)
	}
	if hasDeleteGenerations(f.Calls()) {
		t.Errorf("no devbox generations may be pruned with devbox off; calls: %v", f.Calls())
	}
}

func TestDeepClean_OnlyDevboxEnabled(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	writeStoreConfigToggles(t, env, true, false)
	out, _ := runDeepClean(t, env, f, true, "0d")
	if strings.Contains(out, devboxNote) {
		t.Errorf("devbox sections must be enabled; got:\n%s", out)
	}
	if got := strings.Count(out, floxNote); got != 2 {
		t.Errorf("want 2 flox notes, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "=== Devbox Projects ===\n  (no search dirs configured)\n") {
		t.Errorf("devbox=true with empty search_dirs must print the no-search-dirs line; got:\n%s", out)
	}
	sum := out[strings.Index(out, "=== Summary ==="):]
	for _, want := range []string{"devbox-global:", "devbox-util:", "devbox-projects:"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary missing %s:\n%s", want, sum)
		}
	}
	if strings.Contains(sum, "flox-temp-dirs:") {
		t.Errorf("summary must omit flox-temp-dirs:\n%s", sum)
	}
}

func TestDeepClean_OnlyFloxEnabled(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	writeStoreConfigToggles(t, env, false, true)
	out, _ := runDeepClean(t, env, f, true, "0d")
	if strings.Contains(out, floxNote) {
		t.Errorf("flox sections must be enabled; got:\n%s", out)
	}
	if got := strings.Count(out, devboxNote); got != 3 {
		t.Errorf("want 3 devbox notes, got %d:\n%s", got, out)
	}
	sum := out[strings.Index(out, "=== Summary ==="):]
	if !strings.Contains(sum, "flox-temp-dirs:") {
		t.Errorf("summary missing flox-temp-dirs:\n%s", sum)
	}
	for _, bad := range []string{"devbox-global:", "devbox-util:", "devbox-projects:"} {
		if strings.Contains(sum, bad) {
			t.Errorf("summary must omit %s:\n%s", bad, sum)
		}
	}
}

func TestDeepClean_LiveWithToolsOffStillPrunesCoreAndRunsGC(t *testing.T) {
	env, f := deepcleanFixture(t, true)
	searchDir := filepath.Join(env.Home, "projects")
	if err := os.MkdirAll(searchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(searchDir, "result")
	if err := os.Symlink("/nix/store/xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx-out", result); err != nil {
		t.Fatal(err)
	}
	profilesDir := filepath.Join(env.Home, ".nix-profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleProf := filepath.Join(profilesDir, "old-profile")
	if err := os.Symlink("/nix/store/old", staleProf); err != nil {
		t.Fatal(err)
	}
	orig := lstatModTime
	staleTime := time.Now().AddDate(0, 0, -30)
	lstatModTime = func(string) (time.Time, error) { return staleTime, nil }
	t.Cleanup(func() { lstatModTime = orig })

	profile := makeUserProfile(t, env, f, "fake-user",
		"   1   "+daysAgo(50)+" 12:00:00\n   2   "+daysAgo(1)+" 12:00:00 (current)\n")
	f.AddResponse("nix-env", []string{"--profile", profile, "--delete-generations", "1"}, exec.Result{}, nil)
	writeStoreConfigToggles(t, env, false, false, searchDir)

	out, _ := runDeepClean(t, env, f, false, "14d")

	if findCall(f.Calls(), "nix-env", "--delete-generations", "1", profile) == nil {
		t.Errorf("user profile generation must be pruned; calls: %v", f.Calls())
	}
	if _, err := os.Lstat(result); err == nil {
		t.Errorf("result symlink must be removed; output:\n%s", out)
	}
	if _, err := os.Lstat(staleProf); err == nil {
		t.Errorf("stale nix profile must be removed; output:\n%s", out)
	}
	if findCall(f.Calls(), "sudo", "nix-store", "--gc") == nil {
		t.Errorf("GC must run; calls: %v", f.Calls())
	}
}

func TestDeepClean_DevboxWithoutSearchDirsStillReportsNoSearchDirs(t *testing.T) {
	env, f := deepcleanFixture(t, false)
	writeStoreConfigToggles(t, env, true, true)
	out, _ := runDeepClean(t, env, f, true, "0d")
	if !strings.Contains(out, "=== Devbox Projects ===\n  (no search dirs configured)\n\n") {
		t.Errorf("got:\n%s", out)
	}
}

// ─── audit ─────────────────────────────────────────────────────────────────────

func runAudit(t *testing.T, env Env, f *exec.FakeRunner) string {
	t.Helper()
	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, env)
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	return buf.String()
}

func auditFixture(t *testing.T) (Env, *exec.FakeRunner) {
	t.Helper()
	noSymlinkResolution(t)
	f := exec.NewFakeRunner()
	scriptStoreSize(f, "/dev/disk9", "Volume Used Space: 10.0 GB (10737418240 Bytes)")
	return Env{Home: t.TempDir()}, f
}

func TestAudit_DevboxOffGolden(t *testing.T) {
	env, f := auditFixture(t)
	searchDir := filepath.Join(env.Home, "projects")
	if err := os.MkdirAll(filepath.Join(searchDir, "repo/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStoreConfigToggles(t, env, false, true, searchDir)
	out := runAudit(t, env, f)

	idx := strings.Index(out, "=== Devbox Global ===")
	if idx < 0 {
		t.Fatalf("no Devbox Global section:\n%s", out)
	}
	want := "" +
		"=== Devbox Global ===\n" + devboxNote + "\n\n" +
		"=== Devbox Projects ===\n" + devboxNote + "\n\n" +
		"=== Nix Store ===\n" +
		"Volume used: 10.0 GiB\n"
	if out[idx:] != want {
		t.Errorf("audit devbox-off tail mismatch.\nWANT:\n%q\n\nGOT:\n%q", want, out[idx:])
	}
	for _, c := range f.Calls() {
		if c.Name == "git" || c.Name == "du" {
			t.Errorf("unexpected %s call with devbox off: %v", c.Name, c)
		}
	}
}

func TestAudit_DevboxOffSkipsExistingGlobalProfile(t *testing.T) {
	env, f := auditFixture(t)
	p := filepath.Join(env.Home, ".local/share/devbox/global/default/.devbox/nix/profile")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/g", filepath.Join(p, "default")); err != nil {
		t.Fatal(err)
	}
	writeStoreConfigToggles(t, env, false, false)
	out := runAudit(t, env, f)
	if strings.Contains(out, "devbox-global:") {
		t.Errorf("disabled devbox global profile must not be audited:\n%s", out)
	}
}

func TestAudit_FloxHasNoEffect(t *testing.T) {
	envA, fA := auditFixture(t)
	writeStoreConfigToggles(t, envA, true, false)
	envB, fB := auditFixture(t)
	writeStoreConfigToggles(t, envB, true, true)
	a, b := runAudit(t, envA, fA), runAudit(t, envB, fB)
	if a != b {
		t.Errorf("flox toggle changed audit output.\nflox off:\n%q\nflox on:\n%q", a, b)
	}
}
