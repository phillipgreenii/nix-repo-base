package workspace

import (
	"fmt"
	"sort"
)

// graph holds the workspace dependency graph.
//
//	edges[from][to] = true  means repo `from` has a flake input pointing at
//	                        repo `to` (i.e. `from` depends on `to`).
//	inDegree[name]         = how many other workspace repos depend on this repo.
//	slugOwner[slug]        = the repo name owning that slug. Built during
//	                        construction; surfaced as an error if two distinct
//	                        repos have overlapping slug sets.
type graph struct {
	edges     map[string]map[string]bool
	inDegree  map[string]int
	slugOwner map[string]string
}

// buildGraph constructs the dep graph from the parsed config and a per-repo
// map of inputName -> URL (typically produced by readFlakeInputs across all
// repos in cfg.Repos). Keys in repoInputs that are not declared in
// cfg.Repos are silently ignored (the config is the authoritative membership
// set). Returns an error when two distinct repos have overlapping slug sets
// (which would be a misconfiguration — two repos cannot share a github
// identity).
func buildGraph(cfg *WorkspaceConfig, repoInputs map[string]map[string]string) (*graph, error) {
	g := &graph{
		edges:     make(map[string]map[string]bool),
		inDegree:  make(map[string]int),
		slugOwner: make(map[string]string),
	}
	// Initialize one entry per repo so even repos with no edges show up.
	for name := range cfg.Repos {
		g.edges[name] = make(map[string]bool)
		g.inDegree[name] = 0
	}
	// Populate slugOwner. Reject overlaps.
	for name, rc := range cfg.Repos {
		for slug := range SlugSet(rc) {
			if owner, exists := g.slugOwner[slug]; exists && owner != name {
				return nil, fmt.Errorf("slug %q claimed by both %q and %q", slug, owner, name)
			}
			g.slugOwner[slug] = name
		}
	}
	// Add edges. Skip stray repoInputs keys (not in cfg.Repos) so a caller
	// that hands us a superset doesn't panic on a nil edges map.
	for from, inputs := range repoInputs {
		if _, ok := g.edges[from]; !ok {
			continue
		}
		for _, url := range inputs {
			slug := ExtractGithubSlug(url)
			if slug == "" {
				continue
			}
			to, ok := g.slugOwner[slug]
			if !ok || to == from {
				continue
			}
			if !g.edges[from][to] {
				g.edges[from][to] = true
				g.inDegree[to]++
			}
		}
	}
	return g, nil
}

// selectTerminal picks the terminal repo per design §9. Inputs:
//   - flagTerminal          (highest priority; overrides config when non-empty)
//   - cfg.Workspace.Terminal (optional explicit pick)
//   - g.inDegree           (computed by buildGraph)
//
// Behavior:
//  1. Compute the set of candidates (in-degree == 0).
//  2. If flagTerminal is set, it wins; validate it is a graph node and a sink.
//  3. If cfg.Workspace.Terminal is set:
//     - it must be in inDegree (graph node); else error.
//     - it must be in candidates (in-degree 0); else error.
//     Return it.
//  4. If exactly one candidate, return it.
//  5. If multiple candidates and no explicit terminal, return error with
//     candidate list — user must set [workspace].terminal.
//  6. If zero candidates, the graph has a cycle — return error.
func selectTerminal(cfg *WorkspaceConfig, g *graph, flagTerminal string) (string, error) {
	candidates := make([]string, 0, len(g.inDegree))
	for name, d := range g.inDegree {
		if d == 0 {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)

	// Tier 1: --terminal flag takes highest priority.
	if flagTerminal != "" {
		if _, exists := g.inDegree[flagTerminal]; !exists {
			return "", fmt.Errorf("--terminal %q is not a graph node (no flake.nix?)", flagTerminal)
		}
		if g.inDegree[flagTerminal] > 0 {
			return "", fmt.Errorf("--terminal %q has in-degree %d; cannot be a terminal", flagTerminal, g.inDegree[flagTerminal])
		}
		return flagTerminal, nil
	}

	// Tier 2: config terminal.
	if explicit := cfg.Workspace.Terminal; explicit != "" {
		if _, exists := g.inDegree[explicit]; !exists {
			return "", fmt.Errorf("workspace.terminal %q is not a graph node (no flake.nix?)", explicit)
		}
		if g.inDegree[explicit] > 0 {
			return "", fmt.Errorf("workspace.terminal %q has in-degree %d; cannot be a terminal", explicit, g.inDegree[explicit])
		}
		return explicit, nil
	}

	// Tier 3: auto-detect.
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("dependency cycle: no repo has in-degree 0")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple terminal candidates (%v); set [workspace].terminal in pn-workspace.toml", candidates)
	}
}

// topoSort returns repos in Kahn-topological order — dependencies first,
// terminal last. Within each "level" (set of nodes whose remaining
// in-degree dropped to 0 in the same iteration), the order is stable
// alphabetical for determinism.
//
// Returns an error when the graph has a cycle (some node never reaches
// in-degree 0).
func topoSort(g *graph) ([]string, error) {
	// Reverse Kahn's: build reverse edges to run standard Kahn's on the inverted graph.
	// This gives us dependencies first (high in-degree), terminals last (in-degree 0).
	revEdges := make(map[string]map[string]bool)
	revInDeg := make(map[string]int)
	for n := range g.inDegree {
		revEdges[n] = make(map[string]bool)
		revInDeg[n] = 0
	}
	// Invert edges: if A -> B in original, then B -> A in reversed.
	for from, targets := range g.edges {
		for to := range targets {
			revEdges[to][from] = true
			revInDeg[from]++
		}
	}

	// Standard Kahn's on reversed graph.
	deg := make(map[string]int)
	for n, d := range revInDeg {
		deg[n] = d
	}
	out := make([]string, 0, len(deg))
	for len(out) < len(g.inDegree) {
		// Collect all zero-in-degree nodes for this level; sort
		// alphabetically; emit them in that order.
		level := make([]string, 0)
		for n, d := range deg {
			if d == 0 {
				level = append(level, n)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("dependency cycle: cannot topologically sort remaining repos: %v", remaining(deg))
		}
		sort.Strings(level)
		for _, n := range level {
			out = append(out, n)
			delete(deg, n)
			// Decrement in-degree of every node n points at in reversed graph.
			for to := range revEdges[n] {
				if _, present := deg[to]; present {
					deg[to]--
				}
			}
		}
	}
	return out, nil
}

func remaining(deg map[string]int) []string {
	r := make([]string, 0, len(deg))
	for n := range deg {
		r = append(r, n)
	}
	sort.Strings(r)
	return r
}
