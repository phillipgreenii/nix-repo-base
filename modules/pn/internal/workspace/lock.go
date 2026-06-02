package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Lock is the on-disk pn-workspace.lock representation. It records the derived
// dependency DAG between workspace repos — NOT repo URLs or revisions (URLs
// live in pn-workspace.toml; revisions are irrelevant to the dependency
// structure). Both fields are derived from the terminal flake's lock and
// change only when the inter-repo dependency graph changes.
type Lock struct {
	// Order is every workspace repo key in topological order: dependencies
	// first, the terminal flake last, siblings broken alphabetically.
	Order []string `json:"order"`
	// DependsOn maps a repo key to the sorted workspace repo keys it depends
	// on. Repos with no workspace dependencies are omitted.
	DependsOn map[string][]string `json:"dependsOn"`
}

// ReadLock reads a pn-workspace.lock from path. If the file does not exist,
// returns an empty Lock without error.
func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Lock{DependsOn: make(map[string][]string)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lock %s: %w", path, err)
	}
	lock := &Lock{}
	if err := json.Unmarshal(data, lock); err != nil {
		return nil, fmt.Errorf("parse lock %s: %w", path, err)
	}
	if lock.DependsOn == nil {
		lock.DependsOn = make(map[string][]string)
	}
	return lock, nil
}

// WriteLock writes a pn-workspace.lock to path. Output is deterministic:
// json.Marshal sorts the DependsOn map keys, the dependency slices are stored
// pre-sorted, and Order preserves the topological sequence.
func WriteLock(path string, lock *Lock) error {
	out := struct {
		Order     []string            `json:"order"`
		DependsOn map[string][]string `json:"dependsOn"`
	}{Order: lock.Order, DependsOn: lock.DependsOn}
	if out.Order == nil {
		out.Order = []string{}
	}
	if out.DependsOn == nil {
		out.DependsOn = map[string][]string{}
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
