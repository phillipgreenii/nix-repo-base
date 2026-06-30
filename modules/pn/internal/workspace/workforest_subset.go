package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// filterConfig returns a copy of cfg restricted to the repos in memberSet. The
// [workspace] section is preserved, except that workspace.terminal is cleared
// when the configured terminal repo is not a member (so the subset config stays
// valid — a terminal must name a declared repo). Hooks are preserved verbatim
// (they key on command names, not repos).
func filterConfig(cfg *WorkspaceConfig, memberSet map[string]bool) *WorkspaceConfig {
	out := &WorkspaceConfig{
		Workspace: cfg.Workspace,
		Repos:     make(map[string]RepoConfig, len(memberSet)),
		Hooks:     cfg.Hooks,
	}
	for key, rc := range cfg.Repos {
		if memberSet[key] {
			out.Repos[key] = rc
		}
	}
	if out.Workspace.Terminal != "" && !memberSet[out.Workspace.Terminal] {
		out.Workspace.Terminal = ""
	}
	return out
}

// filterLock returns a copy of lock restricted to the repos in memberSet:
// Order keeps member keys in their original relative order; Repos keeps only
// member entries; Edges keep only those whose Consumer AND Target are both
// members (an edge to an excluded target is dropped). Terminal is cleared when
// it is not a member, so the filtered lock satisfies the lock invariants.
func filterLock(lock *Lock, memberSet map[string]bool) *Lock {
	if lock == nil {
		return emptyLock()
	}
	out := emptyLock()

	for _, k := range lock.Order {
		if memberSet[k] {
			out.Order = append(out.Order, k)
		}
	}
	for k, entry := range lock.Repos {
		if memberSet[k] {
			out.Repos[k] = entry
		}
	}
	for _, e := range lock.Edges {
		if memberSet[e.Consumer] && memberSet[e.Target] {
			out.Edges = append(out.Edges, e)
		}
	}
	if lock.Terminal != "" && memberSet[lock.Terminal] {
		out.Terminal = lock.Terminal
	}
	return out
}

// filterRevLock returns a copy of rl restricted to the repos in memberSet.
func filterRevLock(rl *RevLock, memberSet map[string]bool) *RevLock {
	out := &RevLock{Repos: make(map[string]LockedRepo)}
	if rl == nil {
		return out
	}
	for k, v := range rl.Repos {
		if memberSet[k] {
			out.Repos[k] = v
		}
	}
	return out
}

// excludedDepEdges returns the edges whose Consumer is a member but whose Target
// is excluded from memberSet — i.e. workspace dependencies that the subset drops.
func excludedDepEdges(lock *Lock, memberSet map[string]bool) []LockEdge {
	if lock == nil {
		return nil
	}
	var out []LockEdge
	for _, e := range lock.Edges {
		if memberSet[e.Consumer] && !memberSet[e.Target] {
			out = append(out, e)
		}
	}
	return out
}

// writeConfigTOMLTo serializes cfg to dest atomically (tempfile+rename),
// matching the key order used by writeConfigTOMLAtomic: [workspace] first,
// repos, then hooks. Used to write a subset set's pn-workspace.toml.
func writeConfigTOMLTo(dest string, cfg *WorkspaceConfig) error {
	type orderedConfig struct {
		Workspace WorkspaceSection       `toml:"workspace"`
		Repos     map[string]RepoConfig  `toml:"repos"`
		Hooks     map[string]HookCommand `toml:"hooks,omitempty"`
	}
	out := orderedConfig{
		Workspace: cfg.Workspace,
		Repos:     cfg.Repos,
		Hooks:     cfg.Hooks,
	}
	data, err := toml.Marshal(out)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".pn-config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config (tempfile): %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write config (write): %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (close): %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (rename): %w", err)
	}
	return nil
}
