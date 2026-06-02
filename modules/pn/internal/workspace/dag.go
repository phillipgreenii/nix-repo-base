package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// deriveDAG computes the workspace dependency DAG from the source of truth:
// the inputs each repo declares in its flake.nix. It deliberately does NOT
// read flake.lock, which is a derived artifact that may be absent or stale.
//
// Returns the topological order (dependencies first, terminal last, siblings
// alphabetical) and the adjacency map repoKey -> sorted workspace deps.
func (ws *Workspace) deriveDAG(ctx context.Context) ([]string, map[string][]string, error) {
	declared, err := ws.gatherDeclaredInputs(ctx)
	if err != nil {
		return nil, nil, err
	}
	order, dependsOn := buildDAG(ws.config, declared)
	return order, dependsOn, nil
}

// gatherDeclaredInputs returns, for each workspace repo present on disk, the
// names of the inputs declared in its flake.nix. It evaluates the declared
// `inputs` attrset (`nix eval --file flake.nix inputs --apply attrNames`) — a
// pure read of the source that does not consult the lock and does not fetch or
// evaluate the inputs themselves.
func (ws *Workspace) gatherDeclaredInputs(ctx context.Context) (map[string][]string, error) {
	declared := make(map[string][]string)
	for _, key := range orderedRepoNames(ws.config.Repos) {
		flakePath := filepath.Join(ws.root, key, "flake.nix")
		if _, err := os.Stat(flakePath); err != nil {
			continue // repo not cloned, or not a flake
		}
		res, err := ws.runner.Run(ctx, "nix",
			[]string{"eval", "--json", "--file", flakePath, "inputs", "--apply", "builtins.attrNames"},
			exec.RunOptions{})
		if err != nil {
			return nil, fmt.Errorf("read declared inputs of %s: %w", key, err)
		}
		var names []string
		if err := json.Unmarshal(res.Stdout, &names); err != nil {
			return nil, fmt.Errorf("parse declared inputs of %s: %w", key, err)
		}
		declared[key] = names
	}
	return declared, nil
}

// buildDAG turns each repo's declared input names into the workspace dependency
// DAG. Repo A depends on repo B when A declares an input named B's input-name —
// the same name `--override-input` targets, so the edge mirrors what the build
// actually overrides. Returns the topological order and the adjacency map
// (repos with no workspace deps omitted).
func buildDAG(cfg *WorkspaceConfig, declaredInputs map[string][]string) ([]string, map[string][]string) {
	repoKeys := orderedRepoNames(cfg.Repos)

	// input-name -> repoKey for every workspace repo.
	repoByInputName := make(map[string]string, len(repoKeys))
	for _, k := range repoKeys {
		repoByInputName[cfg.InputNameFor(k)] = k
	}

	dependsOn := make(map[string][]string)
	for _, a := range repoKeys {
		seen := make(map[string]bool)
		var deps []string
		for _, name := range declaredInputs[a] {
			b, ok := repoByInputName[name]
			if !ok || b == a || seen[b] {
				continue
			}
			seen[b] = true
			deps = append(deps, b)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			dependsOn[a] = deps
		}
	}

	return topoSort(repoKeys, dependsOn), dependsOn
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
