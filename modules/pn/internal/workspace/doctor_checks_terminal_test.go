// internal/workspace/doctor_checks_terminal_test.go
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckTerminal_NoTerminalIsError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{
		root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "", lock: emptyLock()}
	if !hasFinding(ws.checkTerminal(context.Background(), env), "terminal-resolvable", SevError) {
		t.Fatal("no terminal should be terminal-resolvable error")
	}
}

func TestCheckTerminal_FollowsViolationIsError(t *testing.T) {
	root := t.TempDir()
	term := filepath.Join(root, "term")
	initRealRepo(t, term)
	// flake.lock where workspace inputs a and b do NOT follow each other.
	lock := `{"nodes":{"root":{"inputs":{"a":"a","b":"b"}},"a":{"inputs":{"b":"b"}},"b":{}}}`
	if err := os.WriteFile(filepath.Join(term, "flake.lock"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{
			Workspace: WorkspaceSection{Terminal: "term"},
			Repos:     map[string]RepoConfig{"term": {URL: "u", Branch: "main"}},
		},
		lock: &Lock{
			Terminal: "term",
			Edges:    []LockEdge{{Consumer: "term", Alias: "a", Target: "x"}, {Consumer: "term", Alias: "b", Target: "y"}},
		},
	}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term", lock: ws.lock}
	if !hasFinding(ws.checkTerminal(context.Background(), env), "follows-correct", SevError) {
		t.Fatal("unfollowed workspace input should be follows-correct error")
	}
}

// Item 3: flake-path-resolves. When the lock's recorded flake_path for a repo
// differs from the path resolved on disk, the wrong flake would be evaluated —
// this is a fixable error whose fix delegates to WriteDerivedLock (pn workspace
// lock) and whose manual hint is "pn workspace lock".
func TestCheckTerminal_FlakePathDriftIsFixableError(t *testing.T) {
	root := t.TempDir()
	dep := filepath.Join(root, "dep")
	initRealRepo(t, dep) // flake.nix lives at repo root on disk

	// ws.lock is nil so resolveFlakePath falls through to the on-disk search,
	// which finds the real "flake.nix" at the repo root.
	if err := os.WriteFile(filepath.Join(dep, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}},
	}
	// env.lock records a DIFFERENT flake_path than the disk resolution → drift.
	envLock := &Lock{Repos: map[string]LockRepoEntry{"dep": {FlakePath: "nix/flake.nix"}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "", lock: envLock}

	fs := ws.checkTerminal(context.Background(), env)
	if !hasFindingForRepo(fs, "flake-path-resolves", "dep", SevError) {
		t.Fatalf("recorded flake_path != on-disk resolution should be a flake-path-resolves error: %+v", fs)
	}
	var fp *Finding
	for i := range fs {
		if fs[i].CheckID == "flake-path-resolves" && fs[i].Repo == "dep" {
			fp = &fs[i]
		}
	}
	if fp == nil || !fp.Fixable || fp.fix == nil {
		t.Fatalf("flake-path-resolves should be fixable (WriteDerivedLock): %+v", fs)
	}
	if fp.Manual != "pn workspace lock" {
		t.Errorf("manual hint = %q, want \"pn workspace lock\"", fp.Manual)
	}
}

// No drift: recorded flake_path matches the on-disk resolution → no finding.
func TestCheckTerminal_FlakePathMatchesIsClean(t *testing.T) {
	root := t.TempDir()
	dep := filepath.Join(root, "dep")
	initRealRepo(t, dep)
	if err := os.WriteFile(filepath.Join(dep, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{
		root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{"dep": {URL: "u", Branch: "main"}}},
	}
	envLock := &Lock{Repos: map[string]LockRepoEntry{"dep": {FlakePath: "flake.nix"}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "", lock: envLock}
	for _, f := range ws.checkTerminal(context.Background(), env) {
		if f.CheckID == "flake-path-resolves" {
			t.Fatalf("matching flake_path must not produce a drift finding: %+v", f)
		}
	}
}
