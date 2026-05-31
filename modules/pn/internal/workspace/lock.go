package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// Lock is the on-disk pn-workspace.lock representation. Repos maps repo
// directory name → locked rev metadata.
type Lock struct {
	Repos map[string]LockedRepo `json:"repos"`
}

// LockedRepo records the resolved state of a single workspace repo.
type LockedRepo struct {
	URL string `json:"url"`
	Rev string `json:"rev"`
}

// ReadLock reads a pn-workspace.lock from path. If the file does not exist,
// returns an empty Lock without error.
func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Lock{Repos: make(map[string]LockedRepo)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lock %s: %w", path, err)
	}
	lock := &Lock{}
	if err := json.Unmarshal(data, lock); err != nil {
		return nil, fmt.Errorf("parse lock %s: %w", path, err)
	}
	if lock.Repos == nil {
		lock.Repos = make(map[string]LockedRepo)
	}
	return lock, nil
}

// WriteLock writes a pn-workspace.lock to path with deterministic key ordering
// (alphabetical by repo name).
func WriteLock(path string, lock *Lock) error {
	// json.Marshal sorts map keys alphabetically by default in Go, so we rely
	// on that for determinism. The explicit sort below is for documentation
	// and to make the ordering invariant visible in code.
	names := make([]string, 0, len(lock.Repos))
	for n := range lock.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	type ordered struct {
		Repos map[string]LockedRepo `json:"repos"`
	}
	out := ordered{Repos: make(map[string]LockedRepo, len(lock.Repos))}
	for _, n := range names {
		out.Repos[n] = lock.Repos[n]
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
