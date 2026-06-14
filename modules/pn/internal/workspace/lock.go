package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// Lock is the on-disk pn-workspace.lock.json representation. It records the
// verbose dependency graph between workspace repos: per-repo metadata,
// per-edge consumer/alias/target triples, the topological iteration order,
// and the terminal repo.
//
// Supersedes the legacy pn-workspace.lock format (Order + DependsOn).
// Schemas are not interconvertible; migration emits a notice and deletes the
// old file.
type Lock struct {
	// Terminal is the workspace terminal repo key (may be empty if unset).
	Terminal string `json:"terminal,omitempty"`
	// Order is every workspace repo key in topological order: dependencies
	// first, the terminal flake last, siblings broken alphabetically.
	Order []string `json:"order"`
	// Repos maps each workspace repo key to its per-repo lock metadata.
	Repos map[string]LockRepoEntry `json:"repos"`
	// Edges is the sorted list of dependency edges between workspace repos.
	// Sorted by (consumer, alias, target).
	Edges []LockEdge `json:"edges"`
}

// LockRepoEntry holds per-repo metadata recorded in the lock.
type LockRepoEntry struct {
	// FlakePath is the relative path to the repo's flake.nix within its
	// directory. Empty when no flake.nix was found or the repo is not a
	// flake input.
	FlakePath string `json:"flake_path,omitempty"`
	// RemoteURL is the canonical remote URL for this repo.
	RemoteURL string `json:"remote_url"`
}

// LockEdge records one directed dependency edge: Consumer declares Alias as a
// flake input that resolves to Target.
type LockEdge struct {
	Consumer string `json:"consumer"`
	Alias    string `json:"alias"`
	Target   string `json:"target"`
}

// LockFileName is the workspace lock filename at the workspace root.
// Supersedes the legacy LockFileNameLegacy.
const LockFileName = "pn-workspace.lock.json"

// LockFileNameLegacy is the old lock filename, checked for migration.
const LockFileNameLegacy = "pn-workspace.lock"

// ParseLock validates the structural invariants of a Lock and returns an error
// describing the first violation found. It does NOT enforce runtime derivation
// errors (missing_terminal, terminal_not_sink, etc.) — those come from
// deriveLock validation errors.
//
// Invariants checked:
//
//	(a) No self-edge: edge.Consumer != edge.Target.
//	(b) Per-consumer alias uniqueness: no two edges share (Consumer, Alias).
//	(c) Edge endpoints exist in Repos: edge.Consumer and edge.Target in Repos.
//	(d) Edge target has a flake path: Repos[edge.Target].FlakePath != "".
//	(e) Terminal validity: if Terminal != "", Terminal appears in Repos.
func ParseLock(lock *Lock) error {
	// (e) Terminal validity.
	if lock.Terminal != "" {
		if _, ok := lock.Repos[lock.Terminal]; !ok {
			return fmt.Errorf("lock invariant violated: terminal %q not found in repos", lock.Terminal)
		}
	}

	// Per-edge checks: iterate once, collecting alias uniqueness state.
	// (b) Per-consumer uniqueness key: (Consumer, Alias) -> Target.
	aliasMap := make(map[[2]string]string)

	for i, e := range lock.Edges {
		// (a) No self-edge.
		if e.Consumer == e.Target {
			return fmt.Errorf("lock invariant violated: self-edge at index %d: consumer == target == %q", i, e.Consumer)
		}
		// (c) Consumer exists in Repos.
		if _, ok := lock.Repos[e.Consumer]; !ok {
			return fmt.Errorf("lock invariant violated: edge[%d] consumer %q not found in repos", i, e.Consumer)
		}
		// (c) Target exists in Repos.
		if _, ok := lock.Repos[e.Target]; !ok {
			return fmt.Errorf("lock invariant violated: edge[%d] target %q not found in repos", i, e.Target)
		}
		// (d) Target has a flake path.
		if lock.Repos[e.Target].FlakePath == "" {
			return fmt.Errorf("lock invariant violated: edge[%d] target %q has empty flake_path", i, e.Target)
		}
		// (b) Per-consumer alias uniqueness.
		key := [2]string{e.Consumer, e.Alias}
		if existing, dup := aliasMap[key]; dup {
			return fmt.Errorf("lock invariant violated: edge[%d] duplicate (consumer=%q, alias=%q): already maps to %q, now also %q",
				i, e.Consumer, e.Alias, existing, e.Target)
		}
		aliasMap[key] = e.Target
	}
	return nil
}

// ReadLock reads a pn-workspace.lock.json from path. If the file does not
// exist, returns an empty Lock without error. If the legacy pn-workspace.lock
// file exists (and the new .json file does not), emits a migration notice to
// stderr.
func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Check for legacy lock file and emit migration notice.
		legacyPath := legacyLockPath(path)
		if legacyPath != "" {
			if _, err2 := os.Stat(legacyPath); err2 == nil {
				fmt.Fprintf(os.Stderr,
					"pn: legacy lock file %s found; run `pn workspace lock` to migrate to %s\n",
					LockFileNameLegacy, LockFileName)
			}
		}
		return emptyLock(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lock %s: %w", path, err)
	}
	lock := &Lock{}
	if err := json.Unmarshal(data, lock); err != nil {
		return nil, fmt.Errorf("parse lock %s: %w", path, err)
	}
	if lock.Repos == nil {
		lock.Repos = make(map[string]LockRepoEntry)
	}
	if lock.Edges == nil {
		lock.Edges = []LockEdge{}
	}
	if lock.Order == nil {
		lock.Order = []string{}
	}
	return lock, nil
}

// WriteLock writes the lock to path in deterministic JSON format:
// repos keys sorted, edges sorted by (consumer, alias, target), order
// preserved as-is (already topological).
func WriteLock(path string, lock *Lock) error {
	type orderedOutput struct {
		Terminal string                   `json:"terminal,omitempty"`
		Order    []string                 `json:"order"`
		Repos    map[string]LockRepoEntry `json:"repos"`
		Edges    []LockEdge               `json:"edges"`
	}

	order := lock.Order
	if order == nil {
		order = []string{}
	}

	repos := lock.Repos
	if repos == nil {
		repos = make(map[string]LockRepoEntry)
	}

	// Sort edges for deterministic output.
	edges := make([]LockEdge, len(lock.Edges))
	copy(edges, lock.Edges)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Consumer != edges[j].Consumer {
			return edges[i].Consumer < edges[j].Consumer
		}
		if edges[i].Alias != edges[j].Alias {
			return edges[i].Alias < edges[j].Alias
		}
		return edges[i].Target < edges[j].Target
	})

	out := orderedOutput{
		Terminal: lock.Terminal,
		Order:    order,
		Repos:    repos,
		Edges:    edges,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lock: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write lock %s: %w", path, err)
	}
	return nil
}

// emptyLock returns a zero-value Lock with initialized (non-nil) collections.
func emptyLock() *Lock {
	return &Lock{
		Repos: make(map[string]LockRepoEntry),
		Edges: []LockEdge{},
		Order: []string{},
	}
}

// legacyLockPath returns the expected legacy lock path given the new lock path,
// or "" if the path doesn't have the expected .json suffix.
func legacyLockPath(newPath string) string {
	const jsonSuffix = ".json"
	if len(newPath) > len(jsonSuffix) && newPath[len(newPath)-len(jsonSuffix):] == jsonSuffix {
		return newPath[:len(newPath)-len(jsonSuffix)]
	}
	return ""
}
