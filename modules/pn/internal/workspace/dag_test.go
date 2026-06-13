package workspace

import (
	"reflect"
	"testing"
)

// TestBuildDAG_OrdersDepsBeforeDependents checks the pure graph builder: an
// edge A->B exists when A declares an input with the same name as B's repo key.
// overlay depends on base; the terminal consumes both.
func TestBuildDAG_OrdersDepsBeforeDependents(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "ziprecruiter"},
		Repos: map[string]RepoConfig{
			"base":         {URL: "github:o/base"},
			"overlay":      {URL: "github:o/overlay"},
			"ziprecruiter": {URL: "github:o/zr"},
		},
	}
	declared := map[string][]string{
		"base":         {"nixpkgs"},
		"overlay":      {"nixpkgs", "base"},
		"ziprecruiter": {"nixpkgs", "base", "overlay"},
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
			"base": {URL: "github:o/base"},
			"bbb":  {URL: "github:o/bbb"},
			"aaa":  {URL: "github:o/aaa"},
			"term": {URL: "github:o/term"},
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
			"a": {URL: "github:o/a"},
			"b": {URL: "github:o/b"},
			"z": {URL: "github:o/z"},
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
