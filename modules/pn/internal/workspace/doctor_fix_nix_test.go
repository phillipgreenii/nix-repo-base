// internal/workspace/doctor_fix_nix_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Item 6: the doctor's nix-touching fixes are otherwise unexercised —
// TestDoctor_ConvergesAfterFix uses a no-flake fixture, so it never drives the
// nix path. These tests use the fsNixRunner model from propagate_test.go (real
// git, `nix` intercepted to mutate the filesystem) to exercise the two fixes
// that reach nix:
//
//   - flake-lock-fresh → Update(--siblings-only) → propagateWorkspaceEdges,
//     which runs `nix flake update --refresh <alias>` and commits the bump.
//   - flake-path-resolves → WriteDerivedLock, which derives the lock via
//     `nix eval` per repo and writes pn-workspace.lock.json.

// consumerFlakeLock builds a minimal flake.lock pinning alias "dep" at rev.
func consumerFlakeLock(rev string) string {
	return `{"version":7,"root":"root","nodes":{` +
		`"root":{"inputs":{"dep":"dep"}},` +
		`"dep":{"locked":{"rev":"` + rev + `","lastModified":1}}}}`
}

// TestDoctorFix_FlakeLockFreshRelocksViaNix drives the flake-lock-fresh fix
// (Update --siblings-only) with a real consumer repo whose flake.lock pins the
// sibling "dep" at an OLD rev. nix is intercepted to rewrite the flake.lock to
// the fresh rev; the fix must run nix flake update --refresh and COMMIT the bump
// so the consumer's flake.lock now pins the new rev on a clean tree.
func TestDoctorFix_FlakeLockFreshRelocksViaNix(t *testing.T) {
	root := t.TempDir()
	const oldRev = "1111111111111111111111111111111111111111"
	const newRev = "2222222222222222222222222222222222222222"

	// Two real repos: consumer (terminal) depends on dep. Neither has an
	// upstream, so Update skips pull/push and only propagate touches nix.
	consumer := filepath.Join(root, "consumer")
	dep := filepath.Join(root, "dep")
	initRealRepo(t, consumer)
	initRealRepo(t, dep)
	if err := os.WriteFile(filepath.Join(consumer, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "flake.lock"), []byte(consumerFlakeLock(oldRev)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, consumer, "add", "-A")
	runGitT(t, consumer, "commit", "-q", "-m", "add flake")
	runGitT(t, dep, "add", "-A")
	runGitT(t, dep, "commit", "-q", "-m", "add flake")

	// A pn-workspace.toml so Open() succeeds; the lock is injected below.
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "consumer"},
		Repos: map[string]RepoConfig{
			"consumer": {URL: "u1", Branch: "main"},
			"dep":      {URL: "u2", Branch: "main"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// nix intercepted: `nix flake update --refresh dep` rewrites consumer's
	// flake.lock to the fresh rev (a real nix would do the same fetch+relock).
	r := &fsNixRunner{real: exec.NewRealRunner(), mutate: func() {
		if err := os.WriteFile(filepath.Join(consumer, "flake.lock"), []byte(consumerFlakeLock(newRev)), 0o644); err != nil {
			t.Fatal(err)
		}
	}}

	ws, err := Open(root, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Inject a lock that matches config (so topoAlpha/effectiveLock short-circuit
	// without nix eval) and carries the consumer→dep edge propagate needs.
	ws.lock = &Lock{
		Terminal: "consumer",
		Order:    []string{"dep", "consumer"},
		Repos:    map[string]LockRepoEntry{"consumer": {FlakePath: "flake.nix"}, "dep": {FlakePath: "flake.nix"}},
		Edges:    []LockEdge{{Consumer: "consumer", Alias: "dep", Target: "dep"}},
	}

	// Build the flake-lock-fresh finding and attach the (nix-touching) fix.
	fs := []Finding{{CheckID: "flake-lock-fresh", Repo: "consumer", Severity: SevError}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "consumer", lock: ws.lock}
	attachFlakeLockFix(ws, env, fs, map[string]bool{"consumer": true})
	if !fs[0].Fixable || fs[0].fix == nil {
		t.Fatalf("expected a fixable flake-lock-fresh finding: %+v", fs[0])
	}

	if err := fs[0].fix(context.Background()); err != nil {
		t.Fatalf("flake-lock-fresh fix: %v", err)
	}

	// nix flake update --refresh dep must have run.
	sawRefresh := false
	for _, a := range r.nixArgs {
		if containsStr(a, "flake") && containsStr(a, "update") && containsStr(a, "--refresh") && containsStr(a, "dep") {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Fatalf("fix must run `nix flake update --refresh dep`; nix calls=%v", r.nixArgs)
	}
	// The bump must be COMMITTED: consumer's flake.lock now pins newRev, on a
	// clean tree, with a bump commit at HEAD.
	got, err := readAliasRevs(filepath.Join(consumer, "flake.lock"), []string{"dep"})
	if err != nil {
		t.Fatal(err)
	}
	if got["dep"] != newRev {
		t.Fatalf("consumer flake.lock pins %q, want the fresh rev %q (bump not committed)", got["dep"], newRev)
	}
	if subj := headSubject(t, consumer); subj != "chore(deps): bump dep 1111111 -> 2222222" {
		t.Errorf("HEAD commit subject = %q, want the bump commit", subj)
	}
	assertCleanTree(t, consumer)
}

// TestDoctorFix_FlakePathResolvesRegensLockViaNix drives the flake-path-resolves
// fix (WriteDerivedLock). WriteDerivedLock derives the lock via `nix eval` per
// repo; nix is intercepted to return each repo's flake inputs. The fix must
// write pn-workspace.lock.json with the CORRECT on-disk flake_path, overwriting
// the drifted value.
func TestDoctorFix_FlakePathResolvesRegensLockViaNix(t *testing.T) {
	root := t.TempDir()
	term := filepath.Join(root, "term")
	base := filepath.Join(root, "base")
	makeFlakeDirs(t, root, "term", "base") // real flake.nix at each repo root

	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"term": {URL: "github:o/term"},
			"base": {URL: "github:o/base"},
		},
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// A DRIFTED lock on disk: term's flake_path recorded as the wrong subdir.
	driftLock := &Lock{
		Terminal: "term",
		Order:    []string{"base", "term"},
		Repos: map[string]LockRepoEntry{
			"term": {FlakePath: "wrong/flake.nix", RemoteURL: "github:o/term"},
			"base": {FlakePath: "flake.nix", RemoteURL: "github:o/base"},
		},
		Edges: []LockEdge{{Consumer: "term", Alias: "my-base", Target: "base"}},
	}
	if err := writeLockAtomic(filepath.Join(root, LockFileName), driftLock); err != nil {
		t.Fatal(err)
	}

	// nix eval intercepted: term depends on base via alias my-base; base has none.
	fullExpr := `is: builtins.mapAttrs (n: v: { url = v.url or null; flake = v.flake or true; }) is`
	evalArgs := func(repo string) []string {
		return []string{"eval", "--json", "--file", filepath.Join(root, repo, "flake.nix"), "inputs", "--apply", fullExpr}
	}
	f := exec.NewFakeRunner()
	f.AddResponse("nix", evalArgs("base"),
		exec.Result{Stdout: []byte(`{"nixpkgs":{"url":"https://github.com/NixOS/nixpkgs.git","flake":true}}`)}, nil)
	f.AddResponse("nix", evalArgs("term"),
		exec.Result{Stdout: []byte(`{"my-base":{"url":"github:o/base","flake":true}}`)}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Open loaded the drifted disk lock into ws.lock; clear it so the regen
	// resolves each repo's flake_path from disk (tier 3) rather than echoing the
	// drift back (resolveFlakePath consults ws.lock first). This mirrors the fix
	// firing precisely when the recorded path no longer matches on-disk reality.
	ws.lock = nil

	// Sanity: on-disk resolution is the real "flake.nix", not the drifted subdir.
	if got := ws.resolveFlakePath("term"); got != "flake.nix" {
		t.Fatalf("precondition: on-disk resolveFlakePath(term) = %q, want %q", got, "flake.nix")
	}

	// Drive the flake-path-resolves fix: WriteDerivedLock (nix-touching).
	fix := func(c context.Context) error { return ws.WriteDerivedLock(c, root) }
	if err := fix(context.Background()); err != nil {
		t.Fatalf("WriteDerivedLock fix: %v", err)
	}

	// The regenerated lock must record term's real on-disk flake_path ("flake.nix").
	got, err := ReadLock(filepath.Join(root, LockFileName))
	if err != nil {
		t.Fatalf("ReadLock regenerated lock: %v", err)
	}
	if got.Repos["term"].FlakePath != "flake.nix" {
		t.Fatalf("regenerated lock term.flake_path = %q, want %q (drift not corrected)",
			got.Repos["term"].FlakePath, "flake.nix")
	}
	_ = term
	_ = base
}
