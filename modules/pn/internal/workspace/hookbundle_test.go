// internal/workspace/hookbundle_test.go
package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hbRepo creates a real git repo (under a symlink-resolved temp dir) with a
// tracked flake.lock and flake.nix, and returns its path.
func hbRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	initRealRepo(t, dir)
	writeFile(t, filepath.Join(dir, "flake.lock"), "{}\n")
	writeFile(t, filepath.Join(dir, "flake.nix"), "{}\n")
	runGitT(t, dir, "add", "flake.lock", "flake.nix")
	runGitT(t, dir, "commit", "-q", "-m", "flake")
	return dir
}

// hbPgDir returns the pg-hooks directory of the checkout at dir: the shared one
// (<common-dir>/pg-hooks) or, with private, the checkout's own
// (<git-dir>/pg-hooks).
func hbPgDir(t *testing.T, dir string, private bool) string {
	t.Helper()
	flag := "--git-common-dir"
	if private {
		flag = "--absolute-git-dir"
	}
	return filepath.Join(runGitT(t, dir, "rev-parse", "--path-format=absolute", flag), "pg-hooks")
}

// hbBundleOpts shapes writeFakeBundle.
type hbBundleOpts struct {
	Private    bool
	Gen        string // default gen-1
	Overrides  []HookOverride
	StampPaths []string
	// Stamp overrides the recorded stamp (default: the current one).
	Stamp string
	// ClonePath overrides the recorded clone_path (default: the common dir).
	ClonePath string
}

// writeFakeBundle lays out <pg-hooks>/<gen>/{bundle,source.json} and the
// `current` pointer exactly as the installer does (a real directory stands in
// for the nix GC root), recording the checkout's current stamp.
func writeFakeBundle(t *testing.T, dir string, o hbBundleOpts) string {
	t.Helper()
	if o.Gen == "" {
		o.Gen = "gen-1"
	}
	pg := hbPgDir(t, dir, o.Private)
	bundle := filepath.Join(pg, o.Gen, "bundle")
	mustMkdir(t, filepath.Join(bundle, "bin"))
	for _, f := range []string{"prek", "pg-hooks-run"} {
		if err := os.WriteFile(filepath.Join(bundle, "bin", f), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[string]any{"repo": "", "stampPaths": o.StampPaths, "stages": []string{"pre-commit"}}
	b, _ := json.Marshal(meta)
	writeFile(t, filepath.Join(bundle, "meta.json"), string(b))

	top := runGitT(t, dir, "rev-parse", "--path-format=absolute", "--show-toplevel")
	stamp := o.Stamp
	if stamp == "" {
		var err error
		stamp, err = hookStamp(top, append([]string{"flake.lock", "flake.nix"}, o.StampPaths...))
		if err != nil {
			t.Fatal(err)
		}
	}
	clone := o.ClonePath
	if clone == "" {
		clone = runGitT(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	ovs := o.Overrides
	if ovs == nil {
		ovs = []HookOverride{}
	}
	src, _ := json.Marshal(map[string]any{
		"stamp": stamp, "overrides": ovs, "clone_path": clone, "built_at": "2026-10-01T00:00:00Z",
	})
	writeFile(t, filepath.Join(pg, o.Gen, "source.json"), string(src))
	writeFile(t, filepath.Join(pg, "current"), o.Gen+"\n")
	return pg
}

func wantState(t *testing.T, dir string, want HookBundleState) HookBundleInfo {
	t.Helper()
	got, info, err := ReadHookBundleState(dir)
	if err != nil {
		t.Fatalf("ReadHookBundleState(%s): %v", dir, err)
	}
	if got != want {
		t.Fatalf("state = %q, want %q (info %+v)", got, want, info)
	}
	return info
}

func TestReadHookBundleState_NotARepoIsAnError(t *testing.T) {
	st, _, err := ReadHookBundleState(t.TempDir())
	if err == nil || st != "" {
		t.Fatalf("a non-repo must return an error and no state; got %q, %v", st, err)
	}
	if _, _, err := ReadHookBundleState(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing directory must return an error")
	}
}

func TestReadHookBundleState_PresentReadsDirectly(t *testing.T) {
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{})
	info := wantState(t, dir, HookBundlePresent)
	if info.Dir != pg || info.Gen != "gen-1" || info.Private || info.Linked {
		t.Errorf("unexpected info for a canonical present bundle: %+v", info)
	}
	if info.Bundle != filepath.Join(pg, "gen-1", "bundle") || info.BuiltAt != "2026-10-01T00:00:00Z" {
		t.Errorf("bundle/built_at not read: %+v", info)
	}
}

func TestReadHookBundleState_MissingAndLegacy(t *testing.T) {
	dir := hbRepo(t)
	wantState(t, dir, HookBundleMissing)

	// A usable legacy config makes it legacy; the file is read in place.
	writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
	wantState(t, dir, HookBundleLegacy)
}

func TestReadHookBundleState_LegacyFromCanonicalClone(t *testing.T) {
	dir := hbRepo(t)
	writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGitT(t, dir, "worktree", "add", "-q", "-b", "feat", wt)
	// The worktree has no config of its own: the canonical clone's counts.
	wantState(t, wt, HookBundleLegacy)
	if _, err := os.Lstat(filepath.Join(wt, ".pre-commit-config.yaml")); err == nil {
		t.Fatal("reading the state must never link a config into the worktree")
	}
}

func TestReadHookBundleState_DanglingBundleIsMissing(t *testing.T) {
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{})
	// The GC root's target was collected: bin/pg-hooks-run is gone.
	if err := os.Remove(filepath.Join(pg, "gen-1", "bundle", "bin", "pg-hooks-run")); err != nil {
		t.Fatal(err)
	}
	wantState(t, dir, HookBundleMissing)
}

func TestReadHookBundleState_Broken(t *testing.T) {
	t.Run("prek not executable", func(t *testing.T) {
		dir := hbRepo(t)
		pg := writeFakeBundle(t, dir, hbBundleOpts{})
		if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "prek"), 0o644); err != nil {
			t.Fatal(err)
		}
		info := wantState(t, dir, HookBundleBroken)
		if !strings.Contains(info.BrokenReason, "not executable") {
			t.Errorf("reason = %q", info.BrokenReason)
		}
	})
	for name, content := range map[string]string{
		"traversal pointer": "../x\n",
		"empty pointer":     "\n",
		"bare gen":          "gen-\n",
		"trailing junk":     "gen-1x\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := hbRepo(t)
			pg := writeFakeBundle(t, dir, hbBundleOpts{})
			writeFile(t, filepath.Join(pg, "current"), content)
			wantState(t, dir, HookBundleBroken)
		})
	}
	t.Run("symlink pointer", func(t *testing.T) {
		dir := hbRepo(t)
		pg := writeFakeBundle(t, dir, hbBundleOpts{})
		writeFile(t, filepath.Join(pg, "real-current"), "gen-1\n")
		if err := os.Remove(filepath.Join(pg, "current")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(pg, "real-current"), filepath.Join(pg, "current")); err != nil {
			t.Fatal(err)
		}
		wantState(t, dir, HookBundleBroken)
	})
}

func TestReadHookBundleState_StaleStampUsesTheIndex(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})

	// An unstaged edit is not in the stamp (it reads the index): still present.
	writeFile(t, filepath.Join(dir, "flake.lock"), "{\"a\":1}\n")
	wantState(t, dir, HookBundlePresent)

	// Staging it changes the stamp.
	runGitT(t, dir, "add", "flake.lock")
	info := wantState(t, dir, HookBundleStale)
	if !info.StampDiffers || len(info.ChangedOverrides) != 0 {
		t.Errorf("want a stamp-only stale; got %+v", info)
	}
}

func TestReadHookBundleState_UnrelatedEditOutsideStampInputsIsPresent(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	writeFile(t, filepath.Join(dir, "other.nix"), "{}\n")
	runGitT(t, dir, "add", "other.nix")
	wantState(t, dir, HookBundlePresent)
}

func TestReadHookBundleState_StampPathsFromMeta(t *testing.T) {
	dir := hbRepo(t)
	writeFile(t, filepath.Join(dir, "hooks.nix"), "{}\n")
	runGitT(t, dir, "add", "hooks.nix")
	runGitT(t, dir, "commit", "-q", "-m", "hooks")
	writeFakeBundle(t, dir, hbBundleOpts{StampPaths: []string{"hooks.nix"}})
	wantState(t, dir, HookBundlePresent)
	writeFile(t, filepath.Join(dir, "hooks.nix"), "{ a = 1; }\n")
	runGitT(t, dir, "add", "hooks.nix")
	wantState(t, dir, HookBundleStale)
}

func TestReadHookBundleState_NoTrackedStampInputsIsNeverStale(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{Stamp: "unknown"})
	runGitT(t, dir, "rm", "-q", "--cached", "flake.lock", "flake.nix")
	wantState(t, dir, HookBundlePresent)
}

func TestReadHookBundleState_Overrides(t *testing.T) {
	setup := func(t *testing.T) (dir, ov string) {
		dir = hbRepo(t)
		ov = filepath.Join(filepath.Dir(dir), "producer")
		initRealRepo(t, ov)
		writeFakeBundle(t, dir, hbBundleOpts{Overrides: []HookOverride{
			{Name: "base", Path: ov, Head: headRev(t, ov), Dirty: false},
		}})
		return dir, ov
	}
	t.Run("matching", func(t *testing.T) {
		dir, _ := setup(t)
		info := wantState(t, dir, HookBundlePresent)
		if len(info.Overrides) != 1 || info.Overrides[0].Name != "base" {
			t.Errorf("overrides not read: %+v", info.Overrides)
		}
	})
	t.Run("head changed", func(t *testing.T) {
		dir, ov := setup(t)
		addCommit(t, ov, "x.txt", "x", "producer change")
		info := wantState(t, dir, HookBundleStale)
		if !reflect.DeepEqual(info.ChangedOverrides, []string{"base"}) || info.StampDiffers {
			t.Errorf("want only override base changed; got %+v", info)
		}
	})
	t.Run("became dirty", func(t *testing.T) {
		dir, ov := setup(t)
		writeFile(t, filepath.Join(ov, "README.md"), "edited\n")
		wantState(t, dir, HookBundleStale)
	})
	t.Run("recorded dirty and still dirty", func(t *testing.T) {
		dir := hbRepo(t)
		ov := filepath.Join(filepath.Dir(dir), "producer")
		initRealRepo(t, ov)
		writeFile(t, filepath.Join(ov, "README.md"), "edited\n")
		writeFakeBundle(t, dir, hbBundleOpts{Overrides: []HookOverride{
			{Name: "base", Path: ov, Head: headRev(t, ov), Dirty: true},
		}})
		wantState(t, dir, HookBundlePresent)
	})
	t.Run("override path gone", func(t *testing.T) {
		dir, ov := setup(t)
		if err := os.RemoveAll(ov); err != nil {
			t.Fatal(err)
		}
		wantState(t, dir, HookBundleStale)
	})
}

func TestReadHookBundleState_Relocated(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{ClonePath: "/somewhere/else/.git"})
	wantState(t, dir, HookBundleRelocated)
}

func TestReadHookBundleState_RelocatedSurvivesSymlinkedPrefix(t *testing.T) {
	// The recorded clone_path names the clone through a symlinked prefix; the
	// bundle is NOT relocated.
	dir := hbRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(dir), link); err != nil {
		t.Fatal(err)
	}
	writeFakeBundle(t, dir, hbBundleOpts{ClonePath: filepath.Join(link, "repo", ".git")})
	wantState(t, dir, HookBundlePresent)
}

func TestReadHookBundleState_Unreachable(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	info := wantState(t, dir, HookBundleUnreachable)
	if info.UnreachableValue != ".githooks" || !strings.HasSuffix(info.UnreachableOrigin, "config") {
		t.Errorf("unreachable detail not read: %+v", info)
	}

	// A core.hooksPath that resolves to the common hooks dir is fine.
	runGitT(t, dir, "config", "--local", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	wantState(t, dir, HookBundlePresent)
}

func TestReadHookBundleState_UnreachableWithoutBundle(t *testing.T) {
	dir := hbRepo(t)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	wantState(t, dir, HookBundleUnreachable)
}

func TestReadHookBundleState_PrecedenceRelocatedOverUnreachableOverBroken(t *testing.T) {
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{ClonePath: "/moved/.git"})
	if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "prek"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	wantState(t, dir, HookBundleRelocated) // a broken, relocated, unreachable clone
	// Un-relocate: unreachable wins over broken.
	writeFakeBundle(t, dir, hbBundleOpts{})
	if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "prek"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantState(t, dir, HookBundleUnreachable)
	runGitT(t, dir, "config", "--local", "--unset", "core.hooksPath")
	wantState(t, dir, HookBundleBroken)
}

func TestReadHookBundleState_BrokenWinsOverStale(t *testing.T) {
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{Stamp: "deadbeef"})
	wantState(t, dir, HookBundleStale)
	if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "pg-hooks-run"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantState(t, dir, HookBundleBroken)
}

func TestReadHookBundleState_LinkedWorktreeSharedAndPrivate(t *testing.T) {
	dir := hbRepo(t)
	shared := writeFakeBundle(t, dir, hbBundleOpts{})
	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGitT(t, dir, "worktree", "add", "-q", "-b", "feat", wt)

	// The worktree sees the shared bundle: present, not private, linked.
	info := wantState(t, wt, HookBundlePresent)
	if !info.Linked || info.Private || info.Dir != shared {
		t.Errorf("worktree on the shared bundle: %+v", info)
	}

	// Its own bundle wins over the shared one.
	priv := writeFakeBundle(t, wt, hbBundleOpts{Private: true})
	if priv == shared {
		t.Fatal("private dir must differ from the shared dir in a linked worktree")
	}
	info = wantState(t, wt, HookBundlePresent)
	if !info.Linked || !info.Private || info.Dir != priv {
		t.Errorf("worktree on its private bundle: %+v", info)
	}
	// The canonical clone still reads the shared bundle.
	info = wantState(t, dir, HookBundlePresent)
	if info.Private || info.Linked || info.Dir != shared {
		t.Errorf("canonical clone: %+v", info)
	}
}

func TestReadHookBundleState_WorktreeDefinitionsDifferFromSharedBundle(t *testing.T) {
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGitT(t, dir, "worktree", "add", "-q", "-b", "feat", wt)
	writeFile(t, filepath.Join(wt, "flake.lock"), "{\"changed\":true}\n")
	runGitT(t, wt, "add", "flake.lock")
	info := wantState(t, wt, HookBundleStale)
	if !info.StampDiffers {
		t.Errorf("a worktree's own staged flake.lock edit must make the shared bundle stale for it: %+v", info)
	}
	wantState(t, dir, HookBundlePresent)
}

func TestPrivateInstallArgs(t *testing.T) {
	got := privateInstallArgs([]string{
		"--override-input", "base", "git+file:///w/set/repo-base",
		"--override-input", "lib", "git+file:///w/set/lib",
	})
	want := []string{"--private", "--override", "base=/w/set/repo-base", "--override", "lib=/w/set/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("privateInstallArgs = %v, want %v", got, want)
	}
	if got := privateInstallArgs(nil); !reflect.DeepEqual(got, []string{"--private"}) {
		t.Errorf("no pins: %v", got)
	}
}

func TestExpandNixRunTokens_InstallArgsOnlyForTheInstallAttr(t *testing.T) {
	v := nixHookVars{
		NixExe:       "nix",
		OverrideArgs: []string{"--override-input", "base", "git+file:///w/s/base"},
		FlakeDir:     "/w/s/consumer",
		InstallArgs:  []string{"--private", "--override", "base=/w/s/base dir"},
	}
	got, _, err := expandNixRunTokens("{nix_run install-pre-commit-hooks}", v)
	if err != nil {
		t.Fatal(err)
	}
	want := "nix run --override-input base 'git+file:///w/s/base' '/w/s/consumer#install-pre-commit-hooks' -- --private --override 'base=/w/s/base dir'"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	other, _, err := expandNixRunTokens("{nix_run something-else}", v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, " -- ") {
		t.Errorf("a different attr must not receive installer args: %q", other)
	}
}
