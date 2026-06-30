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
	ws := &Workspace{root: root, runner: exec.NewFakeRunner(),
		config: &WorkspaceConfig{Repos: map[string]RepoConfig{}}}
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
	ws := &Workspace{root: root, runner: exec.NewRealRunner(),
		config: &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "term"},
			Repos: map[string]RepoConfig{"term": {URL: "u", Branch: "main"}}},
		lock: &Lock{Terminal: "term",
			Edges: []LockEdge{{Consumer: "term", Alias: "a", Target: "x"}, {Consumer: "term", Alias: "b", Target: "y"}}}}
	env := &doctorEnv{ws: ws, mode: "primary", terminal: "term", lock: ws.lock}
	if !hasFinding(ws.checkTerminal(context.Background(), env), "follows-correct", SevError) {
		t.Fatal("unfollowed workspace input should be follows-correct error")
	}
}
