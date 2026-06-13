package workspace

import (
	"fmt"
	"sort"
)

// ValidationError records a non-fatal workspace validation issue that the
// caller should surface to the user. Code is a machine-readable identifier;
// Message is a human-readable description.
type ValidationError struct {
	Code    string
	Message string
}

func (v ValidationError) Error() string { return v.Message }

// resolveTerminal resolves the workspace terminal repo following a 3-tier
// priority:
//
//  1. flagTerminal — if non-empty, validates it exists in repos and returns it.
//  2. cfg.Workspace.Terminal — if set, use it.
//  3. Auto-detect: unique sink among flake repos that is in the same connected
//     component as at least one other flake repo.
//
// After resolution (any tier), if the resolved terminal is non-empty and has
// inbound edges (i.e., it is consumed by another workspace repo), a
// ValidationError with code "terminal_not_sink" is appended to the returned
// validErrs slice.
//
// Returns ("", nil, nil) when auto-detect finds zero or multiple candidates —
// no terminal, no error, no validation warnings.
//
// Returns ("", nil, err) when flagTerminal names an unknown repo.
func resolveTerminal(
	cfg *WorkspaceConfig,
	flagTerminal string,
	edges []LockEdge,
	repos map[string]LockRepoEntry,
) (terminal string, validErrs []ValidationError, err error) {
	// Tier 1: explicit flag.
	if flagTerminal != "" {
		found := false
		if _, ok := repos[flagTerminal]; ok {
			found = true
		} else if cfg != nil && cfg.Repos != nil {
			if _, ok2 := cfg.Repos[flagTerminal]; ok2 {
				found = true
			}
		}
		if !found {
			return "", nil, fmt.Errorf("--terminal %q: repo not found in workspace", flagTerminal)
		}
		terminal = flagTerminal
	}

	// Tier 2: config terminal.
	if terminal == "" && cfg != nil && cfg.Workspace.Terminal != "" {
		terminal = cfg.Workspace.Terminal
	}

	// Tier 3: auto-detect.
	if terminal == "" {
		terminal = autoDetectTerminal(edges, repos)
	}

	// Safety check: terminal_not_sink.
	if terminal != "" {
		consumers := consumersOf(terminal, edges)
		if len(consumers) > 0 {
			validErrs = append(validErrs, ValidationError{
				Code: "terminal_not_sink",
				Message: fmt.Sprintf(
					"configured terminal %q is consumed by repo(s) %v; build/apply will not produce the expected artifact -- set workspace.terminal in pn-workspace.toml to a repo that no other workspace flake consumes",
					terminal, consumers,
				),
			})
		}
	}

	return terminal, validErrs, nil
}

// autoDetectTerminal finds the unique terminal candidate among flake repos.
//
// Algorithm:
//  1. Compute the set of flake repos: repos with non-empty FlakePath.
//  2. Build inbound-edge counts (among flake repos only) from edges.
//  3. Candidates = flake repos with zero inbound edges (sinks in the flake subgraph).
//  4. Filter out isolated candidates: keep only those in the same connected
//     component as at least one other flake repo (i.e., reachable > 0 from that node).
//  5. If exactly one connected sink remains: return it. Otherwise return "".
func autoDetectTerminal(edges []LockEdge, repos map[string]LockRepoEntry) string {
	// Step 1: flake repo set.
	flakeRepos := make(map[string]bool)
	for k, v := range repos {
		if v.FlakePath != "" {
			flakeRepos[k] = true
		}
	}
	if len(flakeRepos) == 0 {
		return ""
	}

	// Step 2: inbound-edge counts among flake repos + adjacency map.
	inbound := make(map[string]int)
	for k := range flakeRepos {
		inbound[k] = 0
	}
	children := make(map[string][]string) // consumer -> targets (flake-to-flake)
	for _, e := range edges {
		if !flakeRepos[e.Consumer] || !flakeRepos[e.Target] {
			continue
		}
		inbound[e.Target]++
		children[e.Consumer] = append(children[e.Consumer], e.Target)
	}

	// Step 3: sinks among flake repos.
	var sinks []string
	for k := range flakeRepos {
		if inbound[k] == 0 {
			sinks = append(sinks, k)
		}
	}
	sort.Strings(sinks)
	if len(sinks) == 0 {
		return "" // cycle
	}

	// Step 4: filter isolated sinks (those with no edges to any other flake repo).
	reachable := reachableFromNode(children, flakeRepos)
	var connected []string
	for _, s := range sinks {
		if reachable[s] > 0 {
			connected = append(connected, s)
		}
	}
	// If all sinks are isolated, the graph is disconnected islands — ambiguous.
	if len(connected) == 0 || len(connected) > 1 {
		return ""
	}

	return connected[0]
}

// reachableFromNode returns, for each flake repo, how many distinct other
// flake repos it can reach transitively via the children adjacency map.
// A count of 0 means the node is isolated (no outgoing edges to other flake repos).
func reachableFromNode(children map[string][]string, flakeRepos map[string]bool) map[string]int {
	counts := make(map[string]int, len(flakeRepos))
	for k := range flakeRepos {
		visited := make(map[string]bool)
		dfsTerminal(k, children, visited)
		delete(visited, k) // don't count self
		counts[k] = len(visited)
	}
	return counts
}

// dfsTerminal is a depth-first traversal recording visited nodes.
func dfsTerminal(node string, children map[string][]string, visited map[string]bool) {
	if visited[node] {
		return
	}
	visited[node] = true
	for _, c := range children[node] {
		dfsTerminal(c, children, visited)
	}
}

// consumersOf returns the sorted list of repo keys that have edges targeting name.
func consumersOf(name string, edges []LockEdge) []string {
	seen := make(map[string]bool)
	for _, e := range edges {
		if e.Target == name {
			seen[e.Consumer] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
