package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
)

// deriveDAG computes the workspace dependency DAG from the terminal flake's
// resolved lock graph (terminalLock = contents of the terminal repo's
// flake.lock).
//
// Returns:
//   - order: every workspace repo key in topological order (dependencies
//     first, the terminal last), with siblings broken alphabetically.
//   - dependsOn: adjacency map repoKey -> sorted workspace repoKeys it depends
//     on. Repos with no workspace dependencies are omitted.
//
// Edges come from the resolved input graph: a workspace input node's inputs
// that resolve (directly, or via a single-element follows path) to another
// workspace input node become a dependency edge. Multi-element follows paths
// are sub-input follows, not direct deps, and are ignored.
func deriveDAG(cfg *WorkspaceConfig, terminalLock []byte) ([]string, map[string][]string, error) {
	terminal, err := cfg.TerminalRepo()
	if err != nil {
		return nil, nil, err
	}

	repoKeys := orderedRepoNames(cfg.Repos)

	// inputName -> repoKey for the non-terminal repos.
	repoByInputName := make(map[string]string, len(repoKeys))
	for _, k := range repoKeys {
		if k == terminal {
			continue
		}
		repoByInputName[cfg.InputNameFor(k)] = k
	}

	var lf lockFile
	if err := json.Unmarshal(terminalLock, &lf); err != nil {
		return nil, nil, fmt.Errorf("parse terminal flake.lock: %w", err)
	}
	root, ok := lf.Nodes["root"]
	if !ok {
		return nil, nil, fmt.Errorf("terminal flake.lock has no root node")
	}

	// nodeKey -> repoKey. The terminal's direct inputs (root.inputs) give the
	// authoritative inputName -> nodeKey mapping for every workspace repo; root
	// itself maps to the terminal repo.
	nodeToRepo := map[string]string{"root": terminal}
	for inputName, raw := range root.Inputs {
		repoKey, isWorkspace := repoByInputName[inputName]
		if !isWorkspace {
			continue
		}
		if nodeKey, ok := resolveFollow(raw); ok {
			nodeToRepo[nodeKey] = repoKey
		}
	}

	// Build adjacency: a workspace node's inputs resolving to another workspace
	// node are dependency edges.
	dependsOn := make(map[string][]string)
	for nodeKey, repoKey := range nodeToRepo {
		node, ok := lf.Nodes[nodeKey]
		if !ok {
			continue
		}
		var deps []string
		seen := make(map[string]bool)
		for _, raw := range node.Inputs {
			target, ok := resolveFollow(raw)
			if !ok {
				continue
			}
			depRepo, isWorkspace := nodeToRepo[target]
			if !isWorkspace || depRepo == repoKey || seen[depRepo] {
				continue
			}
			seen[depRepo] = true
			deps = append(deps, depRepo)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			dependsOn[repoKey] = deps
		}
	}

	return topoSort(repoKeys, dependsOn), dependsOn, nil
}

// resolveFollow resolves a flake.lock input value to a target node key:
//
//	"X"          -> X         (direct dependency)
//	["X"]        -> X         (follows a top-level node = direct dependency)
//	["X","Y"...] -> "", false (sub-input follow; not a direct dep)
func resolveFollow(raw json.RawMessage) (string, bool) {
	if s, ok := asString(raw); ok {
		return s, true
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) == 1 {
		return arr[0], true
	}
	return "", false
}

// topoSort returns repoKeys in dependency order (deps first) via Kahn's
// algorithm, breaking ties alphabetically for determinism. Nodes left over by
// a cycle are appended alphabetically so the result always contains every key.
func topoSort(repoKeys []string, dependsOn map[string][]string) []string {
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
