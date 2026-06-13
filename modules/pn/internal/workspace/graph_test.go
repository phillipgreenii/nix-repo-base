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

func TestSelectTerminal_SingleCandidate(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"base":    1,
		"overlay": 0,
	}}
	cfg := &WorkspaceConfig{}
	got, err := selectTerminal(cfg, g)
	if err != nil {
		t.Fatalf("selectTerminal: %v", err)
	}
	if got != "overlay" {
		t.Errorf("got %q, want overlay", got)
	}
}

func TestSelectTerminal_ExplicitInToml(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"base":     1,
		"overlay":  0,
		"personal": 0,
	}}
	cfg := &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "personal"}}
	got, err := selectTerminal(cfg, g)
	if err != nil {
		t.Fatalf("selectTerminal: %v", err)
	}
	if got != "personal" {
		t.Errorf("got %q, want personal", got)
	}
}

func TestSelectTerminal_AmbiguousNoToml_Error(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"a": 0,
		"b": 0,
	}}
	cfg := &WorkspaceConfig{}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: multiple terminal candidates without explicit terminal")
	}
}

func TestSelectTerminal_ExplicitTerminalIsDependedOn_Error(t *testing.T) {
	g := &graph{inDegree: map[string]int{
		"a": 1, // depended on
		"b": 0,
	}}
	cfg := &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "a"}}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: explicit terminal has in-degree > 0")
	}
}

func TestSelectTerminal_Cycle_Error(t *testing.T) {
	// All repos have in-degree > 0 -> cycle.
	g := &graph{inDegree: map[string]int{
		"a": 1,
		"b": 1,
	}}
	cfg := &WorkspaceConfig{}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: dependency cycle")
	}
}

func TestSelectTerminal_ExplicitNotInGraph_Error(t *testing.T) {
	// ParseConfig validates that workspace.terminal names a declared repo,
	// but selectTerminal is also reachable via hand-constructed configs
	// (unit-test fixtures); cover the branch.
	g := &graph{inDegree: map[string]int{"foo": 0}}
	cfg := &WorkspaceConfig{Workspace: WorkspaceSection{Terminal: "ghost"}}
	_, err := selectTerminal(cfg, g)
	if err == nil {
		t.Fatal("expected error: terminal names a non-graph repo")
	}
}

func TestTopoSort_DepsFirstTerminalLast(t *testing.T) {
	// overlay -> base ; personal -> overlay, personal -> base
	g := &graph{
		edges: map[string]map[string]bool{
			"overlay":  {"base": true},
			"personal": {"base": true, "overlay": true},
			"base":     {},
		},
		inDegree: map[string]int{
			"base":     2,
			"overlay":  1,
			"personal": 0,
		},
	}
	order, err := topoSort(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 {
		t.Fatalf("len(order)=%d, want 3", len(order))
	}
	// "base" must come before "overlay"; both before "personal".
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos["base"] >= pos["overlay"] {
		t.Errorf("base must come before overlay; order=%v", order)
	}
	if pos["overlay"] >= pos["personal"] {
		t.Errorf("overlay must come before personal; order=%v", order)
	}
	if pos["base"] >= pos["personal"] {
		t.Errorf("base must come before personal; order=%v", order)
	}
}

func TestTopoSort_StableByNameWithinLevel(t *testing.T) {
	// Three repos with no edges between them — all at level 0. Should
	// emerge sorted alphabetically.
	g := &graph{
		edges:    map[string]map[string]bool{"a": {}, "b": {}, "c": {}},
		inDegree: map[string]int{"a": 0, "b": 0, "c": 0},
	}
	order, err := topoSort(g)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	for i, n := range order {
		if n != want[i] {
			t.Errorf("order[%d]=%q want %q (full: %v)", i, n, want[i], order)
		}
	}
}
