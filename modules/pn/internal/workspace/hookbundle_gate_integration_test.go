//go:build integration

// Heavy real-git tests moved out of the unit gate: each builds real git
// fixtures. They run in integration-checks.pn-workspace-integration-tests.

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGate_PresentBundleSkipsTheInstaller(t *testing.T) {
	w, dir := gateWS(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	if sc := fireInstall(t, w); len(sc) != 0 {
		t.Fatalf("a present, current bundle must skip the installer; got %d sh calls: %+v", len(sc), sc)
	}
}

func TestInstallGate_RunsForMissingBundle(t *testing.T) {
	w, _ := gateWS(t)
	if sc := fireInstall(t, w); len(sc) != 1 {
		t.Fatalf("no bundle: the installer must run once; got %d sh calls", len(sc))
	}
}

func TestInstallGate_RunsForStaleStamp(t *testing.T) {
	w, dir := gateWS(t)
	writeFakeBundle(t, dir, hbBundleOpts{})
	writeFile(t, filepath.Join(dir, "flake.lock"), "{\"bump\":1}\n")
	runGitT(t, dir, "add", "flake.lock")
	if sc := fireInstall(t, w); len(sc) != 1 {
		t.Fatalf("stale stamp: the installer must run once; got %d sh calls", len(sc))
	}
}

func TestInstallGate_RunsForBrokenAndRelocated(t *testing.T) {
	t.Run("broken", func(t *testing.T) {
		w, dir := gateWS(t)
		pg := writeFakeBundle(t, dir, hbBundleOpts{})
		if err := os.Chmod(filepath.Join(pg, "gen-1", "bundle", "bin", "prek"), 0o644); err != nil {
			t.Fatal(err)
		}
		if sc := fireInstall(t, w); len(sc) != 1 {
			t.Fatalf("broken bundle: want 1 sh call, got %d", len(sc))
		}
	})
	t.Run("relocated", func(t *testing.T) {
		w, dir := gateWS(t)
		writeFakeBundle(t, dir, hbBundleOpts{ClonePath: "/moved/.git"})
		if sc := fireInstall(t, w); len(sc) != 1 {
			t.Fatalf("relocated clone: want 1 sh call, got %d", len(sc))
		}
	})
}

func TestInstallGate_OverrideChangeReinstalls(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, ov string){
		"head changed": func(t *testing.T, ov string) { addCommit(t, ov, "x.txt", "x", "producer change") },
		"became dirty": func(t *testing.T, ov string) { writeFile(t, filepath.Join(ov, "README.md"), "edited\n") },
	} {
		t.Run(name, func(t *testing.T) {
			w, dir := gateWS(t)
			ov := filepath.Join(filepath.Dir(w.root), "producer-"+strings.ReplaceAll(name, " ", "-"))
			initRealRepo(t, ov)
			writeFakeBundle(t, dir, hbBundleOpts{Overrides: []HookOverride{
				{Name: "base", Path: ov, Head: headRev(t, ov), Dirty: false},
			}})
			if sc := fireInstall(t, w); len(sc) != 0 {
				t.Fatalf("unchanged overrides must skip; got %d sh calls", len(sc))
			}
			mutate(t, ov)
			if sc := fireInstall(t, w); len(sc) != 1 {
				t.Fatalf("a changed override must reinstall; got %d sh calls", len(sc))
			}
		})
	}
}

// R4: there is no legacy gate. An old .pre-commit-config.yaml, even one that is
// a live /nix/store symlink, is not a bundle, so the installer still runs.
func TestInstallGate_OldConfigDoesNotSkipTheInstaller(t *testing.T) {
	t.Run("plain old config file runs", func(t *testing.T) {
		w, dir := gateWS(t)
		writeFile(t, filepath.Join(dir, ".pre-commit-config.yaml"), "repos: []\n")
		if sc := fireInstall(t, w); len(sc) != 1 {
			t.Fatalf("an old config file is not a bundle: want 1 sh call, got %d", len(sc))
		}
	})
	t.Run("live config symlink runs", func(t *testing.T) {
		target := existingNixStoreFile(t)
		if target == "" {
			t.Skip("no /nix/store files available in this environment")
		}
		w, dir := gateWS(t)
		if err := os.Symlink(target, filepath.Join(dir, ".pre-commit-config.yaml")); err != nil {
			t.Fatal(err)
		}
		if sc := fireInstall(t, w); len(sc) != 1 {
			t.Fatalf("a live old config symlink is not a bundle: want 1 sh call, got %d", len(sc))
		}
	})
}

func TestInstallSetHooks_LinkedWorktreeGetsPrivateAndPins(t *testing.T) {
	w, _, setA := setWS(t)
	sc := fireInstall(t, w)
	if len(sc) != 1 {
		t.Fatalf("want 1 sh call, got %d: %+v", len(sc), sc)
	}
	want := "nix run --override-input base 'git+file://" + filepath.Join(w.root, "b") + "' '" + setA +
		"#install-pre-commit-hooks' -- --private --override 'base=" + filepath.Join(w.root, "b") + "'"
	if got := sc[0].Args[1]; got != want {
		t.Errorf("install command in a linked worktree:\n got  %q\n want %q", got, want)
	}
	if sc[0].Opts.Dir != setA {
		t.Errorf("must run in the set worktree; Dir = %q, want %q", sc[0].Opts.Dir, setA)
	}
}

func TestInstallSetHooks_CanonicalCloneGetsNeitherPrivateNorPins(t *testing.T) {
	lk := &Lock{
		Repos: map[string]LockRepoEntry{
			"a": {FlakePath: "flake.nix", RemoteURL: "github:o/a"},
			"b": {FlakePath: "flake.nix", RemoteURL: "github:o/b"},
		},
		Edges: []LockEdge{{Consumer: "a", Alias: "base", Target: "b"}},
	}
	w := openHookWS(t,
		"[repos.a]\nurl=\"github:o/a\"\n[[repos.a.hooks]]\nwhen=[\"post-clone\"]\nrun=[\"{nix_run install-pre-commit-hooks}\"]\n[repos.b]\nurl=\"github:o/b\"\n",
		[]string{"b"}, lk)
	dirA := filepath.Join(w.root, "a")
	initRealRepo(t, dirA)
	// a repo with no flake.nix is not-enrolled and skips the installer
	writeFile(t, filepath.Join(dirA, "flake.nix"), "{}\n")
	sc := fireInstall(t, w)
	if len(sc) != 1 {
		t.Fatalf("want 1 sh call, got %d", len(sc))
	}
	if got := sc[0].Args[1]; strings.Contains(got, " -- ") || strings.Contains(got, "--private") {
		t.Errorf("a canonical clone must not get installer args: %q", got)
	}
}

func TestInstallGate_LinkedWorktreeNeedsItsOwnPrivateBundle(t *testing.T) {
	w, canonical, setA := setWS(t)
	// The shared bundle (canonical) is present; the set has none of its own.
	writeFakeBundle(t, canonical, hbBundleOpts{})
	if sc := fireInstall(t, w); len(sc) != 1 {
		t.Fatalf("a shared bundle does not give the set its private one; want 1 sh call, got %d", len(sc))
	}
	// Once the set has a private bundle that matches, the hook is skipped.
	writeFakeBundle(t, setA, hbBundleOpts{Private: true})
	if sc := fireInstall(t, w); len(sc) != 0 {
		t.Fatalf("a present private bundle must skip; got %d sh calls", len(sc))
	}
	// A producer change in the set (HEAD moves for a recorded override) -> stale.
	ov := filepath.Join(w.root, "b")
	initRealRepo(t, ov)
	writeFakeBundle(t, setA, hbBundleOpts{Private: true, Overrides: []HookOverride{
		{Name: "base", Path: ov, Head: headRev(t, ov), Dirty: false},
	}})
	if sc := fireInstall(t, w); len(sc) != 0 {
		t.Fatalf("matching override: want skip, got %d sh calls", len(sc))
	}
	addCommit(t, ov, "hook.txt", "v2", "producer change in the set")
	if sc := fireInstall(t, w); len(sc) != 1 {
		t.Fatalf("producer change must rebuild the private bundle; got %d sh calls", len(sc))
	}
}
