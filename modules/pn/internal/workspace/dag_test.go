package workspace

import (
	"reflect"
	"testing"
)

// TestDeriveDAG_OrdersDepsBeforeDependents builds the workspace DAG from a
// terminal flake.lock node graph and checks both the topological order
// (dependencies first, terminal last) and the adjacency (repo -> workspace
// deps). overlay follows base; the terminal (ziprecruiter) consumes both.
func TestDeriveDAG_OrdersDepsBeforeDependents(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "ziprecruiter"},
		Repos: map[string]RepoConfig{
			"base":         {InputName: "phillipgreenii-nix-base"},
			"overlay":      {InputName: "ovl"},
			"ziprecruiter": {},
		},
	}
	lock := []byte(`{
	  "nodes": {
	    "root": { "inputs": { "phillipgreenii-nix-base": "phillipgreenii-nix-base", "ovl": "ovl" } },
	    "phillipgreenii-nix-base": { "inputs": {} },
	    "ovl": { "inputs": { "phillipgreenii-nix-base": ["phillipgreenii-nix-base"] } }
	  }
	}`)

	order, dependsOn, err := deriveDAG(cfg, lock)
	if err != nil {
		t.Fatalf("deriveDAG: %v", err)
	}

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

// TestDeriveDAG_SiblingsAlphabeticalTiebreak verifies that independent repos at
// the same dependency depth come out in a deterministic (alphabetical) order.
func TestDeriveDAG_SiblingsAlphabeticalTiebreak(t *testing.T) {
	cfg := &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: "term"},
		Repos: map[string]RepoConfig{
			"base": {InputName: "base"},
			"bbb":  {InputName: "bbb"},
			"aaa":  {InputName: "aaa"},
			"term": {},
		},
	}
	// aaa and bbb both follow base; term consumes all three.
	lock := []byte(`{
	  "nodes": {
	    "root": { "inputs": { "base": "base", "aaa": "aaa", "bbb": "bbb" } },
	    "base": { "inputs": {} },
	    "aaa": { "inputs": { "base": ["base"] } },
	    "bbb": { "inputs": { "base": ["base"] } }
	  }
	}`)

	order, _, err := deriveDAG(cfg, lock)
	if err != nil {
		t.Fatalf("deriveDAG: %v", err)
	}
	want := []string{"base", "aaa", "bbb", "term"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}
