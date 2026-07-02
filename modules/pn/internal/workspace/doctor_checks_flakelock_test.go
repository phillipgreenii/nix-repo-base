// internal/workspace/doctor_checks_flakelock_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckFlakeLockFresh_StaleIsError(t *testing.T) {
	root := t.TempDir()
	consumer := filepath.Join(root, "consumer")
	initRealRepo(t, consumer)
	// consumer's flake.lock pins input "dep" at an OLD rev.
	old := "1111111111111111111111111111111111111111"
	lock := `{"nodes":{"root":{"inputs":{"dep":"dep"}},"dep":{"locked":{"rev":"` + old + `"}}}}`
	if err := os.WriteFile(filepath.Join(consumer, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "2222222222222222222222222222222222222222"
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{
			"consumer": {URL: "u1", Branch: "main"}, "dep": {URL: "u2", Branch: "main"},
		}},
		lock: &Lock{
			Repos: map[string]LockRepoEntry{"consumer": {FlakePath: "flake.nix"}, "dep": {FlakePath: "flake.nix"}},
			Edges: []LockEdge{{Consumer: "consumer", Alias: "dep", Target: "dep"}},
		},
	}
	env := &doctorEnv{
		ws: ws, mode: "primary", lock: ws.lock,
		refRev: map[string]string{"dep": want, "consumer": "x"}, skipped: map[string]bool{},
	}
	fs := ws.checkFlakeLockFresh(context.Background(), env)
	if !hasFindingForRepo(fs, "flake-lock-fresh", "consumer", SevError) {
		t.Fatalf("stale flake.lock should be error: %+v", fs)
	}
	// The remedy relocks ONLY sibling inputs (not a full nix flake update), so
	// both the auto-fix and the copy-pasteable hint use --siblings-only.
	var fixable *Finding
	for i := range fs {
		if fs[i].Repo == "consumer" && fs[i].Fixable {
			fixable = &fs[i]
			break
		}
	}
	if fixable == nil {
		t.Fatalf("expected a fixable flake-lock-fresh finding for consumer: %+v", fs)
	}
	if fixable.Manual != "pn workspace update --siblings-only" {
		t.Errorf("Manual hint = %q, want the siblings-only relock command", fixable.Manual)
	}
}

// TestAttachFlakeLockFix_FixRunsSiblingsOnly guards the fix WIRING, not just the
// hint string: running the attached fix closure must drive an update that SKIPS
// update-locks.sh. A regression that kept the Manual hint but dropped
// SiblingsOnly from the Update call would pass the hint assertion above but fail
// here (update-locks.sh would run). The script exists on disk (mkUpdateLocks),
// so its absence from the call log is attributable to SiblingsOnly.
func TestAttachFlakeLockFix_FixRunsSiblingsOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pn-workspace.toml"), `
[workspace]
terminal = "foo"

[repos.foo]
url = "github:owner/foo"
`)
	f := exec.NewFakeRunner()
	foo := filepath.Join(root, "foo")
	mkUpdateLocks(t, foo)
	// In-place, no-upstream, no-edges flow: isDirty probes → no upstream →
	// propagation no-op → (skip update-locks) → capture HEAD.
	f.AddResponse("git", []string{"-C", foo, "diff", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "diff", "--cached", "--quiet"}, exec.Result{}, nil)
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "--abbrev-ref", "@{u}"}, exec.Result{ExitCode: 128}, &exec.CommandError{Name: "git", Result: exec.Result{ExitCode: 128}})
	f.AddResponse("git", []string{"-C", foo, "rev-parse", "HEAD"}, exec.Result{Stdout: []byte("abc0000000000000000000000000000000000000\n")}, nil)

	ws, err := Open(root, f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fs := []Finding{{CheckID: "flake-lock-fresh", Repo: "foo", Severity: SevError}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "foo"}
	attachFlakeLockFix(ws, env, fs, map[string]bool{"foo": true})
	if !fs[0].Fixable || fs[0].fix == nil {
		t.Fatalf("expected a fixable finding with a fix closure: %+v", fs[0])
	}
	if err := fs[0].fix(context.Background()); err != nil {
		t.Fatalf("fix closure: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "./update-locks.sh" {
			t.Fatalf("doctor fix must run --siblings-only (no update-locks.sh); calls=%v", f.Calls())
		}
	}
}
