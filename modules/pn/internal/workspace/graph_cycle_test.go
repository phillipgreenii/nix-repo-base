package workspace

import (
	"reflect"
	"strings"
	"testing"
)

// Regression guard: a flake input in each of two workspace repos pointing at
// the other (repo-base -> overlay and overlay -> repo-base) makes discovery
// fail with a dependency cycle. Dropping the repo-base -> overlay input must
// leave a graph that sorts.
func TestTopoSort_TwoRepoFlakeInputCycle(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"nix-repo-base": {URL: "github:o/nix-repo-base"},
		"nix-overlay":   {URL: "github:o/nix-overlay"},
	}}

	cyclic := map[string]map[string]string{
		"nix-overlay":   {"phillipgreenii-nix-repo-base": "github:o/nix-repo-base"},
		"nix-repo-base": {"phillipgreenii-nix-overlay": "github:o/nix-overlay"},
	}
	g, err := buildGraph(cfg, cyclic)
	if err != nil {
		t.Fatalf("buildGraph(cyclic): %v", err)
	}
	if _, err := topoSort(g); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("topoSort(cyclic) error = %v, want a dependency cycle error", err)
	}

	fixed := map[string]map[string]string{
		"nix-overlay":   {"phillipgreenii-nix-repo-base": "github:o/nix-repo-base"},
		"nix-repo-base": {"nixpkgs-unstable": "github:NixOS/nixpkgs/nixpkgs-unstable"},
	}
	g, err = buildGraph(cfg, fixed)
	if err != nil {
		t.Fatalf("buildGraph(fixed): %v", err)
	}
	order, err := topoSort(g)
	if err != nil {
		t.Fatalf("topoSort(fixed): %v", err)
	}
	if want := []string{"nix-repo-base", "nix-overlay"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
