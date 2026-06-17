package workspace

import (
	"sort"
)

// buildDAG turns each repo's declared input names into the workspace dependency
// DAG. Repo A depends on repo B when A declares an input with the same name as
// B's repo key (directory name). Returns the topological order and the adjacency
// map (repos with no workspace deps omitted).
//
// NOTE: buildDAG uses the OLD gatherDeclaredInputs-style data (input names,
// not URLs). It is retained for buildGraph/tree.go internals that still use
// the slug/remote-matching approach. Edge discovery for the lock command uses
// buildEdges (edges.go) instead.
func buildDAG(cfg *WorkspaceConfig, declaredInputs map[string][]string) ([]string, map[string][]string) {
	// Alpha (not topoAlpha): buildDAG constructs the dependency graph that
	// topoAlpha consumes — using topoAlpha here would be circular.
	repoKeys := orderedRepoNames(cfg.Repos)

	// repo key -> bool for every workspace repo (used for membership tests).
	repoSet := make(map[string]bool, len(repoKeys))
	for _, k := range repoKeys {
		repoSet[k] = true
	}

	dependsOn := make(map[string][]string)
	for _, a := range repoKeys {
		seen := make(map[string]bool)
		var deps []string
		for _, name := range declaredInputs[a] {
			// An input matches a workspace repo when its name equals the repo key.
			if !repoSet[name] || name == a || seen[name] {
				continue
			}
			seen[name] = true
			deps = append(deps, name)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			dependsOn[a] = deps
		}
	}

	return topoSortByDeps(repoKeys, dependsOn), dependsOn
}

// topoSort returns repoKeys in dependency order (deps first) via Kahn's
// algorithm, breaking ties alphabetically for determinism. Nodes left over by
// a cycle are appended alphabetically so the result always contains every key.
func topoSortByDeps(repoKeys []string, dependsOn map[string][]string) []string {
	inDegree := make(map[string]int, len(repoKeys))
	dependents := make(map[string][]string) // dep -> repos depending on it
	for _, k := range repoKeys {
		inDegree[k] = 0
	}
	for repo, deps := range dependsOn {
		inDegree[repo] = len(deps)
		for _, d := range deps {
			dependents[d] = append(dependents[d], repo)
		}
	}

	var ready []string
	for _, k := range repoKeys {
		if inDegree[k] == 0 {
			ready = append(ready, k)
		}
	}
	sort.Strings(ready)

	var order []string
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		next := dependents[n]
		sort.Strings(next)
		for _, d := range next {
			inDegree[d]--
			if inDegree[d] == 0 {
				ready = append(ready, d)
				sort.Strings(ready)
			}
		}
	}

	if len(order) < len(repoKeys) {
		inOrder := make(map[string]bool, len(order))
		for _, k := range order {
			inOrder[k] = true
		}
		var rest []string
		for _, k := range repoKeys {
			if !inOrder[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		order = append(order, rest...)
	}
	return order
}
