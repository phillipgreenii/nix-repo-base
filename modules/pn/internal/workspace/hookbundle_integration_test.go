//go:build integration

// Heavy real-git tests moved out of the unit gate: each builds real git
// fixtures. They run in integration-checks.pn-workspace-integration-tests.

package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadHookBundleState_NotARepoIsAnError(t *testing.T) {
	st, _, err := ReadHookBundleState(t.Context(), t.TempDir())
	if err == nil || st != "" {
		t.Fatalf("a non-repo must return an error and no state; got %q, %v", st, err)
	}
	if _, _, err := ReadHookBundleState(t.Context(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing directory must return an error")
	}
}

func TestReadHookBundleState_PresentReadsDirectly(t *testing.T) {
	t.Parallel()
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

func TestReadHookBundleState_MissingWithNoBundle(t *testing.T) {
	t.Parallel()
	dir := hbRepo(t)
	wantState(t, dir, HookBundleMissing)
}

// R4: there is no legacy state. A clone that holds only an old
// .pre-commit-config.yaml reads missing, however usable the file is.
func TestReadHookBundleState_OldConfigIsNotABundle(t *testing.T) {
	t.Parallel()
	dir := hbRepo(t)
	writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
	wantState(t, dir, HookBundleMissing)

	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGitT(t, dir, "worktree", "add", "-q", "-b", "feat", wt)
	// The canonical clone's config does not count for the worktree either, and
	// reading the state must never link a config into it.
	wantState(t, wt, HookBundleMissing)
	if _, err := os.Lstat(filepath.Join(wt, ".pre-commit-config.yaml")); err == nil {
		t.Fatal("reading the state must never link a config into the worktree")
	}
}

func TestReadHookBundleState_DanglingBundleIsMissing(t *testing.T) {
	t.Parallel()
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{})
	// The GC root's target was collected: bin/pg-hooks-run is gone.
	if err := os.Remove(filepath.Join(pg, "gen-1", "bundle", "bin", "pg-hooks-run")); err != nil {
		t.Fatal(err)
	}
	wantState(t, dir, HookBundleMissing)
}

func TestReadHookBundleState_Broken(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	writeFile(t, filepath.Join(dir, "other.nix"), "{}\n")
	runGitT(t, dir, "add", "other.nix")
	wantState(t, dir, HookBundlePresent)
}

func TestReadHookBundleState_StampPathsFromMeta(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	dir := hbRepo(t)
	writeFakeBundle(t, dir, hbBundleOpts{ClonePath: "/somewhere/else/.git"})
	wantState(t, dir, HookBundleRelocated)
}

func TestReadHookBundleState_RelocatedSurvivesSymlinkedPrefix(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	dir := hbRepo(t)
	runGitT(t, dir, "config", "--local", "core.hooksPath", ".githooks")
	wantState(t, dir, HookBundleUnreachable)
}

func TestReadHookBundleState_PrecedenceRelocatedOverUnreachableOverBroken(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	dir := hbRepo(t)
	pg := writeFakeBundle(t, dir, hbBundleOpts{Stamp: "deadbeef"})
	wantState(t, dir, HookBundleStale)
	if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "pg-hooks-run"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantState(t, dir, HookBundleBroken)
}

func TestReadHookBundleState_LinkedWorktreeSharedAndPrivate(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
