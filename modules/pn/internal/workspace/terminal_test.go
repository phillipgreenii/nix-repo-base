package workspace

import (
	"testing"
)

// makeEdgeLock builds a Lock with the given edges and flake paths for edge targets.
func makeEdgeLock(edges []LockEdge, flakePaths map[string]string, allKeys []string) *Lock {
	repos := make(map[string]LockRepoEntry)
	for _, k := range allKeys {
		fp := flakePaths[k]
		repos[k] = LockRepoEntry{FlakePath: fp, RemoteURL: "github:o/" + k}
	}
	return &Lock{
		Repos: repos,
		Edges: edges,
		Order: allKeys,
	}
}

// cfg with terminal set
func makeConfig(terminal string, repoKeys ...string) *WorkspaceConfig {
	repos := make(map[string]RepoConfig)
	for _, k := range repoKeys {
		repos[k] = RepoConfig{URL: "github:o/" + k}
	}
	return &WorkspaceConfig{
		Workspace: WorkspaceSection{Terminal: terminal},
		Repos:     repos,
	}
}

// TestResolveTerminal_AutoDetect: unique sink that depends on all other flake repos -> set.
func TestResolveTerminal_AutoDetect(t *testing.T) {
	// topology: base <- overlay <- terminal
	// terminal has no inbound edges (is a sink), depends on overlay
	// overlay has one inbound edge (from terminal), depends on base
	// base has one inbound edge (from overlay)
	edges := []LockEdge{
		{Consumer: "terminal", Alias: "overlay-input", Target: "overlay"},
		{Consumer: "overlay", Alias: "base-input", Target: "base"},
	}
	allKeys := []string{"base", "overlay", "terminal"}
	flakePaths := map[string]string{"base": "flake.nix", "overlay": "flake.nix", "terminal": "flake.nix"}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if term != "terminal" {
		t.Errorf("terminal = %q, want %q", term, "terminal")
	}
}

// TestResolveTerminal_MultiCandidate: 3 sinks, none dominates -> terminal empty, no terminal_not_sink error.
func TestResolveTerminal_MultiCandidate(t *testing.T) {
	// topology: three independent repos, no edges
	allKeys := []string{"a", "b", "c"}
	flakePaths := map[string]string{"a": "flake.nix", "b": "flake.nix", "c": "flake.nix"}
	lock := makeEdgeLock(nil, flakePaths, allKeys)
	cfg := makeConfig("", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", nil, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	// No validation errors (no terminal was resolved, so no terminal_not_sink check)
	for _, ve := range validErrs {
		if ve.Code == "terminal_not_sink" {
			t.Errorf("unexpected terminal_not_sink error when no terminal resolved")
		}
	}
	if term != "" {
		t.Errorf("terminal = %q, want empty (multiple candidates)", term)
	}
}

// TestResolveTerminal_ZeroCandidate: no flake repos -> terminal empty, silently.
func TestResolveTerminal_ZeroCandidate(t *testing.T) {
	// All repos have empty FlakePath -> no candidates
	allKeys := []string{"a", "b"}
	lock := makeEdgeLock(nil, nil, allKeys)
	cfg := makeConfig("", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", nil, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if term != "" {
		t.Errorf("terminal = %q, want empty", term)
	}
}

// TestResolveTerminal_IsolatedFlakeNotCandidate: isolated flake (FlakePath set but no edges) is not auto-detected as terminal.
func TestResolveTerminal_IsolatedFlakeNotCandidate(t *testing.T) {
	// "isolated" has flake.nix but no edges in/out
	// "base" has flake.nix, "consumer" depends on base
	edges := []LockEdge{
		{Consumer: "consumer", Alias: "base-input", Target: "base"},
	}
	allKeys := []string{"base", "consumer", "isolated"}
	flakePaths := map[string]string{
		"base":     "flake.nix",
		"consumer": "flake.nix",
		"isolated": "flake.nix",
	}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	// With two sinks (consumer, isolated) and "isolated" being truly isolated (not in same component as consumer+base),
	// only "consumer" is in the connected component containing other edges.
	// So terminal should be "consumer".
	if term != "consumer" {
		t.Errorf("terminal = %q, want %q", term, "consumer")
	}
}

// TestResolveTerminal_ConfigWinsOverAutoDetect: config terminal overrides auto-detect.
func TestResolveTerminal_ConfigWinsOverAutoDetect(t *testing.T) {
	// Even though auto-detect would pick "terminal", config says "other"
	// (but "other" is also a sink, so no terminal_not_sink)
	edges := []LockEdge{
		{Consumer: "terminal", Alias: "base-input", Target: "base"},
	}
	allKeys := []string{"base", "other", "terminal"}
	flakePaths := map[string]string{"base": "flake.nix", "other": "flake.nix", "terminal": "flake.nix"}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("other", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	// "other" is a sink (no inbound edges), so no terminal_not_sink
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if term != "other" {
		t.Errorf("terminal = %q, want %q", term, "other")
	}
}

// TestResolveTerminal_FlagWinsOverConfig: flag overrides config.
func TestResolveTerminal_FlagWinsOverConfig(t *testing.T) {
	edges := []LockEdge{
		{Consumer: "terminal", Alias: "base-input", Target: "base"},
	}
	allKeys := []string{"base", "other", "terminal"}
	flakePaths := map[string]string{"base": "flake.nix", "other": "flake.nix", "terminal": "flake.nix"}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("other", allKeys...)

	// flag = "terminal" overrides config "other"
	term, validErrs, err := resolveTerminal(cfg, "terminal", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	if len(validErrs) != 0 {
		t.Errorf("expected no validation errors, got %v", validErrs)
	}
	if term != "terminal" {
		t.Errorf("terminal = %q, want %q", term, "terminal")
	}
}

// TestResolveTerminal_FlagUnknownRepo: flag with unknown repo -> error.
func TestResolveTerminal_FlagUnknownRepo(t *testing.T) {
	allKeys := []string{"base", "terminal"}
	flakePaths := map[string]string{"base": "flake.nix", "terminal": "flake.nix"}
	lock := makeEdgeLock(nil, flakePaths, allKeys)
	cfg := makeConfig("", allKeys...)

	_, _, err := resolveTerminal(cfg, "nonexistent", nil, lock.Repos)
	if err == nil {
		t.Fatal("expected error for unknown flag repo, got nil")
	}
}

// TestResolveTerminal_TerminalNotSink_MonorepodCase: terminal=nix-personal but homelab consumes it.
func TestResolveTerminal_TerminalNotSink_MonorepodCase(t *testing.T) {
	// Mirrors the real monorepod state:
	// pn-workspace.toml has terminal = 'nix-personal'
	// but homelab has an edge: homelab -> nix-personal
	// So nix-personal has an inbound edge and is NOT a sink.
	edges := []LockEdge{
		{Consumer: "homelab", Alias: "nix-personal", Target: "nix-personal"},
	}
	allKeys := []string{"homelab", "nix-personal"}
	flakePaths := map[string]string{
		"homelab":      "flake.nix",
		"nix-personal": "flake.nix",
	}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("nix-personal", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	// Should resolve to "nix-personal" (from config)
	if term != "nix-personal" {
		t.Errorf("terminal = %q, want %q", term, "nix-personal")
	}
	// Should emit terminal_not_sink validation error
	found := false
	for _, ve := range validErrs {
		if ve.Code == "terminal_not_sink" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected terminal_not_sink validation error, got %v", validErrs)
	}
}

// TestResolveTerminal_FlagPointingAtNonSink: --terminal pointing at non-sink also triggers terminal_not_sink.
func TestResolveTerminal_FlagPointingAtNonSink(t *testing.T) {
	// "base" is consumed by "consumer", so base is not a sink.
	edges := []LockEdge{
		{Consumer: "consumer", Alias: "base-input", Target: "base"},
	}
	allKeys := []string{"base", "consumer"}
	flakePaths := map[string]string{"base": "flake.nix", "consumer": "flake.nix"}
	lock := makeEdgeLock(edges, flakePaths, allKeys)
	cfg := makeConfig("", allKeys...)

	term, validErrs, err := resolveTerminal(cfg, "base", edges, lock.Repos)
	if err != nil {
		t.Fatalf("resolveTerminal: %v", err)
	}
	if term != "base" {
		t.Errorf("terminal = %q, want %q", term, "base")
	}
	// Should emit terminal_not_sink
	found := false
	for _, ve := range validErrs {
		if ve.Code == "terminal_not_sink" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected terminal_not_sink validation error, got %v", validErrs)
	}
}
