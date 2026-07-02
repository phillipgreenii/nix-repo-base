package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// RevLockFileName is the on-disk filename for pn-workspace.revs.json — the
// per-repo URL + revision record. Distinct from pn-workspace.lock (origin's
// Lock, which records the DAG between workspace repos, not individual revs).
//
// Two locks serve two needs:
//   - Lock (pn-workspace.lock): the dependency DAG (Order + DependsOn).
//     Drives build sequencing. Changes only when the inter-repo graph changes.
//   - RevLock (pn-workspace.revs.json): per-repo URL + Rev snapshot for
//     reproducibility. Drives --override-input pinning in NixCommand and
//     other lock-based override paths. Changes whenever any repo is pulled.
const RevLockFileName = "pn-workspace.revs.json"

// RevLock is the on-disk per-repo URL+Rev snapshot. It records the resolved
// state of each workspace repo for reproducible builds — distinct from Lock
// which records the DAG ordering for build sequencing.
type RevLock struct {
	Repos map[string]LockedRepo `json:"repos"`
}

// LockedRepo records the resolved state of a single workspace repo: the URL
// it was fetched from and the commit revision it pointed at.
type LockedRepo struct {
	URL string `json:"url"`
	Rev string `json:"rev"`
}

// ReadRevLock reads a pn-workspace.revs.json from path. If the file does not
// exist, returns an empty RevLock without error.
func ReadRevLock(path string) (*RevLock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &RevLock{Repos: make(map[string]LockedRepo)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rev lock %s: %w", path, err)
	}
	rl := &RevLock{}
	if err := json.Unmarshal(data, rl); err != nil {
		return nil, fmt.Errorf("parse rev lock %s: %w", path, err)
	}
	if rl.Repos == nil {
		rl.Repos = make(map[string]LockedRepo)
	}
	return rl, nil
}

// WriteRevLock writes a pn-workspace.revs.json to path, with deterministic
// key ordering so JSON output is reproducible across runs.
func WriteRevLock(path string, rl *RevLock) error {
	if rl == nil {
		return fmt.Errorf("nil RevLock")
	}
	names := make([]string, 0, len(rl.Repos))
	for n := range rl.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	type ordered struct {
		Repos map[string]LockedRepo `json:"repos"`
	}
	out := ordered{Repos: make(map[string]LockedRepo, len(rl.Repos))}
	for _, n := range names {
		out.Repos[n] = rl.Repos[n]
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rev lock: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write rev lock %s: %w", path, err)
	}
	return nil
}
