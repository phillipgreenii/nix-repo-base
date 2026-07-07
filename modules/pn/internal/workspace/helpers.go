package workspace

import (
	"os"
	"path/filepath"
	"sort"
)

// orderedRepoNames returns the names of repos in alphabetical order so that
// per-repo subprocess loops produce deterministic call sequences (and
// deterministic output for status/tree-style verbs).
func orderedRepoNames(repos map[string]RepoConfig) []string {
	names := make([]string, 0, len(repos))
	for n := range repos {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// overrideOpts configures overrideInputArgsFor.
type overrideOpts struct {
	// ExcludeRepo omits one specific repo key. Used by flake-check, where the
	// repo under test is the flake being evaluated and must not override itself.
	ExcludeRepo string
	// OverridePaths maps repo key -> absolute path, replacing the default clone
	// location for that repo.
	OverridePaths map[string]string
}

// dirExists reports whether p exists and is a directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// fileExists reports whether p exists and is a regular file (not a directory).
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// overrideInputArgsFor returns --override-input flags for the given consumer
// repo, using the workspace lock's per-edge aliases. For each LockEdge where
// Consumer == consumer and the target directory exists on disk, emits:
//
//	--override-input <alias> git+file://<target_dir>
//
// The opts.ExcludeRepo field skips edges whose Target matches. The
// opts.OverridePaths map replaces the default clone dir for specific targets.
// Results are sorted by alias for deterministic output.
//
// When the lock has no edges for the consumer (no lock yet, or consumer has no
// workspace deps), returns an empty slice.
func (ws *Workspace) overrideInputArgsFor(consumer string, opts overrideOpts) []string {
	return ws.overrideInputArgsForLock(ws.lock, consumer, opts)
}

// overrideInputArgsForLock is overrideInputArgsFor keyed on an explicit lock
// rather than ws.lock, so callers that need a freshly derived lock (e.g. hook
// fan-out via effectiveLock) get overrides even when ws.lock is empty/stale.
func (ws *Workspace) overrideInputArgsForLock(lk *Lock, consumer string, opts overrideOpts) []string {
	if ws == nil || lk == nil {
		return []string{}
	}

	// Collect edges for this consumer, sorted by alias.
	type edgeEntry struct {
		alias  string
		target string
	}
	var relevant []edgeEntry
	for _, e := range lk.Edges {
		if e.Consumer != consumer {
			continue
		}
		if opts.ExcludeRepo != "" && e.Target == opts.ExcludeRepo {
			continue
		}
		relevant = append(relevant, edgeEntry{alias: e.Alias, target: e.Target})
	}
	// Sort by alias for determinism.
	sort.Slice(relevant, func(i, j int) bool {
		return relevant[i].alias < relevant[j].alias
	})

	out := make([]string, 0, 3*len(relevant))
	for _, e := range relevant {
		dir := filepath.Join(ws.root, e.target)
		if ov, ok := opts.OverridePaths[e.target]; ok {
			dir = ov
		}
		if !dirExists(dir) {
			continue
		}
		out = append(out, "--override-input", e.alias, "git+file://"+dir)
	}
	return out
}

// workspaceInputNamesFromEdges returns the aliases that consumer uses for its
// workspace dependencies, as recorded in the lock's edge set. Used for
// checkFollows — the aliases are the flake input names the terminal declares.
func (ws *Workspace) workspaceInputNamesFromEdges(consumer string) []string {
	if ws == nil || ws.lock == nil {
		return nil
	}
	var names []string
	for _, e := range ws.lock.Edges {
		if e.Consumer == consumer {
			names = append(names, e.Alias)
		}
	}
	sort.Strings(names)
	return names
}

// workspaceDisplayNamesFromEdges maps alias → target repo key for the given
// consumer's edges in the lock. Used by treeAllInputs to display workspace
// repos by directory name rather than their lock key.
func (ws *Workspace) workspaceDisplayNamesFromEdges(consumer string) map[string]string {
	m := make(map[string]string)
	if ws == nil || ws.lock == nil {
		return m
	}
	for _, e := range ws.lock.Edges {
		if e.Consumer == consumer {
			m[e.Alias] = e.Target
		}
	}
	return m
}
