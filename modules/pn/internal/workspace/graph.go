package workspace

import "fmt"

// graph holds the workspace dependency graph.
//
//   edges[from][to] = true  means repo `from` has a flake input pointing at
//                           repo `to` (i.e. `from` depends on `to`).
//   inDegree[name]         = how many other workspace repos depend on this repo.
//   slugOwner[slug]        = the repo name owning that slug. Built during
//                           construction; surfaced as an error if two distinct
//                           repos have overlapping slug sets.
type graph struct {
	edges     map[string]map[string]bool
	inDegree  map[string]int
	slugOwner map[string]string
}

// buildGraph constructs the dep graph from the parsed config and a per-repo
// map of inputName -> URL (typically produced by readFlakeInputs across all
// repos). Returns an error when two distinct repos have overlapping slug
// sets (which would be a misconfiguration — two repos cannot share a github
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
	// Add edges.
	for from, inputs := range repoInputs {
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
