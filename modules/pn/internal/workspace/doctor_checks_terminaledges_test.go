// internal/workspace/doctor_checks_terminaledges_test.go
package workspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

func TestCheckTerminalEdges(t *testing.T) {
	twoRepos := map[string]RepoConfig{
		"term": {URL: "github:o/term", Branch: "main"},
		"dep":  {URL: "github:o/dep", Branch: "main"},
	}
	tests := []struct {
		name      string
		repos     map[string]RepoConfig
		terminal  string
		cloned    bool
		edges     []LockEdge
		nilLock   bool
		wantFlag  bool
		wantInMsg string
	}{
		{name: "terminal with no edges is an error", repos: twoRepos, terminal: "term", cloned: true, wantFlag: true},
		{
			name: "terminal with an edge is fine", repos: twoRepos, terminal: "term", cloned: true,
			edges: []LockEdge{{Consumer: "term", Alias: "dep", Target: "dep"}},
		},
		{
			name: "edge from another consumer does not count", repos: twoRepos, terminal: "term", cloned: true,
			edges: []LockEdge{{Consumer: "dep", Alias: "x", Target: "term"}}, wantFlag: true,
		},
		{
			name: "single-repo workspace is silent", terminal: "term", cloned: true,
			repos: map[string]RepoConfig{"term": {URL: "github:o/term"}},
		},
		{name: "no terminal is silent", repos: twoRepos, terminal: "", cloned: true},
		{name: "terminal not cloned is silent", repos: twoRepos, terminal: "term", cloned: false},
		{name: "nil lock is silent", repos: twoRepos, terminal: "term", cloned: true, nilLock: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.cloned && tc.terminal != "" {
				initRealRepo(t, filepath.Join(root, tc.terminal))
			}
			ws := &Workspace{root: root, runner: exec.NewFakeRunner(), config: &WorkspaceConfig{Repos: tc.repos}}
			lock := &Lock{Terminal: tc.terminal, Edges: tc.edges}
			if tc.nilLock {
				lock = nil
			}
			env := &doctorEnv{ws: ws, mode: "primary", terminal: tc.terminal, lock: lock}
			fs := ws.checkTerminalEdges(context.Background(), env)
			if got := hasFindingForRepo(fs, terminalEdgesCheckID, tc.terminal, SevError); got != tc.wantFlag {
				t.Fatalf("error finding = %v, want %v: %+v", got, tc.wantFlag, fs)
			}
			if tc.wantFlag && !contains([]byte(fs[0].Manual), "mirror_urls") {
				t.Errorf("Manual should suggest mirror_urls: %q", fs[0].Manual)
			}
		})
	}
}
