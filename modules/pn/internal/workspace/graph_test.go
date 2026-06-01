package workspace

import "testing"

func TestBuildGraph_SimpleSingleEdge(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"base":    {URL: "github:o/base"},
		"overlay": {URL: "github:o/overlay"},
	}}
	repoInputs := map[string]map[string]string{
		"overlay": {"phillipgreenii-nix-base": "github:o/base"},
		"base":    {}, // no out-edges
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["base"] != 1 {
		t.Errorf("base in-degree = %d, want 1", g.inDegree["base"])
	}
	if g.inDegree["overlay"] != 0 {
		t.Errorf("overlay in-degree = %d, want 0", g.inDegree["overlay"])
	}
	if !g.edges["overlay"]["base"] {
		t.Error("expected edge overlay -> base")
	}
}

func TestBuildGraph_MultiRemoteIdentity(t *testing.T) {
	// Repo "lib" has two remotes; "consumer-a" uses one, "consumer-b" uses
	// the other. Both should resolve to "lib".
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"lib": {Remotes: []Remote{
			{Name: "origin", URL: "github:o/lib"},
			{Name: "mirror", URL: "https://github.com/o/lib-mirror"},
		}},
		"consumer-a": {URL: "github:o/consumer-a"},
		"consumer-b": {URL: "github:o/consumer-b"},
	}}
	repoInputs := map[string]map[string]string{
		"consumer-a": {"lib": "github:o/lib"},
		"consumer-b": {"lib": "https://github.com/o/lib-mirror"},
		"lib":        {},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["lib"] != 2 {
		t.Errorf("lib in-degree = %d, want 2", g.inDegree["lib"])
	}
	if !g.edges["consumer-a"]["lib"] || !g.edges["consumer-b"]["lib"] {
		t.Error("expected both consumers to point at lib")
	}
}

func TestBuildGraph_SelfEdgeDropped(t *testing.T) {
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"foo": {URL: "github:o/foo"},
	}}
	repoInputs := map[string]map[string]string{
		"foo": {"self": "github:o/foo"},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["foo"] != 0 {
		t.Errorf("self-edge should be dropped; in-degree = %d", g.inDegree["foo"])
	}
}

func TestBuildGraph_AmbiguousSlugSets_Error(t *testing.T) {
	// Two distinct repos with overlapping slug sets — should never happen
	// in practice but surfaces as an error rather than silent pick.
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"a": {URL: "github:o/dup"},
		"b": {Slug: "o/dup"},
	}}
	_, err := buildGraph(cfg, map[string]map[string]string{})
	if err == nil {
		t.Fatal("expected error: two repos share a slug")
	}
}

func TestBuildGraph_StrayRepoInputsKey_Ignored(t *testing.T) {
	// repoInputs has a key for a repo not in cfg.Repos; should be ignored
	// without panic.
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"foo": {URL: "github:o/foo"},
	}}
	repoInputs := map[string]map[string]string{
		"foo":     {},
		"unknown": {"some-input": "github:o/foo"}, // stray
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if _, exists := g.edges["unknown"]; exists {
		t.Error("stray repo should not appear in edges")
	}
}

func TestBuildGraph_DuplicateInputsToSameSibling_OneEdge(t *testing.T) {
	// Two distinct input names in the same repo both resolving to the same
	// sibling should result in one edge (in-degree=1), not two.
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"lib":      {URL: "github:o/lib"},
		"consumer": {URL: "github:o/consumer"},
	}}
	repoInputs := map[string]map[string]string{
		"consumer": {
			"lib-primary": "github:o/lib",
			"lib-alias":   "github:o/lib",
		},
	}
	g, err := buildGraph(cfg, repoInputs)
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if g.inDegree["lib"] != 1 {
		t.Errorf("duplicate inputs to same sibling should produce in-degree 1; got %d", g.inDegree["lib"])
	}
}

func TestBuildGraph_IsolatedRepoIsStillAVertex(t *testing.T) {
	// A repo with no in-edges and no out-edges must still appear in both
	// edges and inDegree maps so callers (selectTerminal, topoSort) see
	// it as a graph node.
	cfg := &WorkspaceConfig{Repos: map[string]RepoConfig{
		"loner": {URL: "github:o/loner"},
	}}
	g, err := buildGraph(cfg, map[string]map[string]string{})
	if err != nil {
		t.Fatalf("buildGraph: %v", err)
	}
	if _, ok := g.edges["loner"]; !ok {
		t.Error("isolated repo must be in edges map")
	}
	if _, ok := g.inDegree["loner"]; !ok {
		t.Error("isolated repo must be in inDegree map")
	}
}
