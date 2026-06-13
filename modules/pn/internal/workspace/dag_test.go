package workspace

import (
	"reflect"
	"testing"
)

// TestBuildDAG_OrdersDepsBeforeDependents checks the pure graph builder: an
// edge A->B exists when A declares an input named B's input-name. overlay
// follows base; the terminal consumes both.
func TestBuildDAG_OrdersDepsBeforeDependents(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "ziprecruiter"},
		Repos: map[string]RepoConfig{
			"base":         {InputName: "phillipgreenii-nix-base"},
			"overlay":      {InputName: "ovl"},
			"ziprecruiter": {},
		},
	}
	declared := map[string][]string{
		"base":         {"nixpkgs"},
		"overlay":      {"nixpkgs", "phillipgreenii-nix-base"},
		"ziprecruiter": {"nixpkgs", "phillipgreenii-nix-base", "ovl"},
	}

	order, dependsOn := buildDAG(cfg, declared)

	wantOrder := []string{"base", "overlay", "ziprecruiter"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
	wantDeps := map[string][]string{
		"overlay":      {"base"},
		"ziprecruiter": {"base", "overlay"},
	}
	if !reflect.DeepEqual(dependsOn, wantDeps) {
		t.Errorf("dependsOn = %v, want %v", dependsOn, wantDeps)
	}
}

// TestBuildDAG_SiblingsAlphabeticalTiebreak verifies deterministic ordering of
// independent repos at the same depth.
func TestBuildDAG_SiblingsAlphabeticalTiebreak(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"base": {InputName: "base"},
			"bbb":  {InputName: "bbb"},
			"aaa":  {InputName: "aaa"},
			"term": {},
		},
	}
	declared := map[string][]string{
		"aaa":  {"base"},
		"bbb":  {"base"},
		"term": {"base", "aaa", "bbb"},
	}

	order, _ := buildDAG(cfg, declared)
	want := []string{"base", "aaa", "bbb", "term"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// TestBuildDAG_CycleFallback: a dependency cycle still yields every repo in the
// order (the topo-sort fallback appends the cyclic nodes alphabetically).
func TestBuildDAG_CycleFallback(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "z"},
		Repos: map[string]RepoConfig{
			"a": {InputName: "a"},
			"b": {InputName: "b"},
			"z": {},
		},
	}
	declared := map[string][]string{
		"a": {"b"}, // a <-> b cycle
		"b": {"a"},
		"z": {"a"},
	}
	order, _ := buildDAG(cfg, declared)
	if len(order) != 3 {
		t.Fatalf("order should contain all repos despite the cycle; got %v", order)
	}
	seen := map[string]bool{}
	for _, k := range order {
		seen[k] = true
	}
	for _, k := range []string{"a", "b", "z"} {
		if !seen[k] {
			t.Errorf("order missing %q: %v", k, order)
		}
	}
}
