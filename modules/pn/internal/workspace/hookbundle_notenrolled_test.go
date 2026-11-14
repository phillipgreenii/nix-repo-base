// internal/workspace/hookbundle_notenrolled_test.go
package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// hbNoFlakeRepo is a real git repo with no flake.nix anywhere.
func hbNoFlakeRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	initRealRepo(t, dir)
	return dir
}

func wantHooks(t *testing.T, dir string, state HookBundleState, hooks string) HookBundleInfo {
	t.Helper()
	info := wantState(t, dir, state)
	if info.Hooks != hooks {
		t.Fatalf("hooks = %q, want %q (state %s, info %+v)", info.Hooks, hooks, state, info)
	}
	return info
}

func TestReadHookBundleState_NoFlakeNoBundleIsNotEnrolled(t *testing.T) {
	dir := hbNoFlakeRepo(t)
	info := wantHooks(t, dir, HookBundleNotEnrolled, HookBundleHooksNA)
	if info.NotEnrolledReason != "no-flake" {
		t.Errorf("reason = %q, want no-flake", info.NotEnrolledReason)
	}
}

func TestReadHookBundleState_FlakeInOneSubdirectoryIsMissing(t *testing.T) {
	dir := hbNoFlakeRepo(t)
	mustMkdir(t, filepath.Join(dir, "nix"))
	writeFile(t, filepath.Join(dir, "nix", "flake.nix"), "{}\n")
	wantHooks(t, dir, HookBundleMissing, HookBundleHooksSkipped)
	// two candidate subdirectories are ambiguous: pgh_flake_dir does not pick one
	mustMkdir(t, filepath.Join(dir, "other"))
	writeFile(t, filepath.Join(dir, "other", "flake.nix"), "{}\n")
	wantHooks(t, dir, HookBundleNotEnrolled, HookBundleHooksNA)
}

func TestReadHookBundleState_StaleHooksPathOutranksNoFlake(t *testing.T) {
	dir := hbNoFlakeRepo(t)
	runGitT(t, dir, "config", "core.hooksPath", "/nonexistent/old-clone/.git/hooks")
	wantHooks(t, dir, HookBundleUnreachable, HookBundleHooksSkipped)
}

func TestReadHookBundleState_OptOutOutranksEveryState(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	runGitT(t, dir, "config", "pg-hooks.enabled", "false")
	info := wantHooks(t, dir, HookBundleNotEnrolled, HookBundleHooksNA)
	if !info.OptedOut || info.NotEnrolledReason != "opted-out" {
		t.Errorf("want opted-out, got %+v", info)
	}
	runGitT(t, dir, "config", "core.hooksPath", "/nonexistent/old-clone/.git/hooks")
	wantHooks(t, dir, HookBundleNotEnrolled, HookBundleHooksNA)
}

func TestReadHookBundleState_FlagSpellingsAndInvalidValue(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	for _, v := range []string{"False", "0", "no", "off"} {
		runGitT(t, dir, "config", "pg-hooks.enabled", v)
		wantHooks(t, dir, HookBundleNotEnrolled, HookBundleHooksNA)
	}
	for _, v := range []string{"true", "maybe"} {
		runGitT(t, dir, "config", "pg-hooks.enabled", v)
		wantHooks(t, dir, HookBundlePresent, HookBundleHooksRuns)
	}
}

func TestReadHookBundleState_HooksKeyPerState(t *testing.T) {
	dir := hbRepo(t)
	wantHooks(t, dir, HookBundleMissing, HookBundleHooksSkipped)
	pg := writeFakeBundle(t, dir, hbBundleOpts{})
	wantHooks(t, dir, HookBundlePresent, HookBundleHooksRuns)

	writeFakeBundle(t, dir, hbBundleOpts{Stamp: "not-the-current-stamp"})
	wantHooks(t, dir, HookBundleStale, HookBundleHooksRuns)

	writeFakeBundle(t, dir, hbBundleOpts{ClonePath: "/old/place/.git"})
	wantHooks(t, dir, HookBundleRelocated, HookBundleHooksRuns)

	writeFakeBundle(t, dir, hbBundleOpts{})
	if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "prek"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantHooks(t, dir, HookBundleBroken, HookBundleHooksFailing)

	writeFile(t, filepath.Join(pg, "current"), "bogus\n")
	wantHooks(t, dir, HookBundleBroken, HookBundleHooksSkipped)
}

func TestInstallHookUpToDate_NotEnrolledSkipsTheInstaller(t *testing.T) {
	t.Run("opted out with no bundle", func(t *testing.T) {
		dir := hbRepo(t)
		runGitT(t, dir, "config", "pg-hooks.enabled", "false")
		if !installHookUpToDate(dir) {
			t.Error("an opted-out clone must not run the installer on every apply")
		}
	})
	t.Run("no flake", func(t *testing.T) {
		if !installHookUpToDate(hbNoFlakeRepo(t)) {
			t.Error("a clone with no flake has nothing to install")
		}
	})
	t.Run("enrolled with no bundle still installs", func(t *testing.T) {
		if installHookUpToDate(hbRepo(t)) {
			t.Error("a missing bundle must run the installer")
		}
	})
}
