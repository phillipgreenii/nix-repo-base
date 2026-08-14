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

// resolvedOverride is one `--override-input` the workspace lock implies for a
// consumer: the flake input alias the consumer declares, the workspace repo that
// alias targets, and the LOCAL directory the override points nix at.
type resolvedOverride struct {
	alias  string
	target string // the [repos.<key>] workspace repo key
	dir    string // the local checkout nix is pointed at (override path or canonical)
}

// url is the flake URL form of the override's directory — the exact string passed
// as the `--override-input` value, so a recorded override and the flag that was
// actually emitted can never disagree about the form.
func (o resolvedOverride) url() string { return "git+file://" + o.dir }

// resolveOverridesFor is resolveOverridesForLock against ws.lock.
func (ws *Workspace) resolveOverridesFor(consumer string, opts overrideOpts) []resolvedOverride {
	return ws.resolveOverridesForLock(ws.lock, consumer, opts)
}

// resolveOverridesForLock is the SINGLE derivation of a consumer's override set,
// which both the emitted `--override-input` flags (overrideInputArgs) and the
// applied-state's record of what an apply overrode (overriddenInputs) project
// from. One derivation is load-bearing: `pb`'s gate condition 2 is SKIPPED for a
// repo the apply recorded as overridden (agent-support ADR 0046's revision), so a
// record that disagreed with the flags would either skip the condition for a
// genuinely lock-built input (fail-OPEN, the pg2-ft60a defect) or enforce it
// against a repo the build never read from the lock.
//
// For each LockEdge where Consumer == consumer and the target directory EXISTS on
// disk, one entry is returned, sorted by alias for deterministic output.
// opts.ExcludeRepo skips edges whose Target matches; opts.OverridePaths replaces
// the default clone dir for specific targets. When the lock has no edges for the
// consumer (no lock yet, or the consumer has no workspace deps), the result is
// empty.
//
// The dirExists filter is why the override set is NOT simply the edge set: an edge
// whose clone is missing yields no override (nix resolves that input from the lock
// instead), which is exactly the state condition 2 still has to police.
func (ws *Workspace) resolveOverridesForLock(lk *Lock, consumer string, opts overrideOpts) []resolvedOverride {
	if ws == nil || lk == nil {
		return nil
	}
	var out []resolvedOverride
	for _, e := range lk.Edges {
		if e.Consumer != consumer {
			continue
		}
		if opts.ExcludeRepo != "" && e.Target == opts.ExcludeRepo {
			continue
		}
		dir := filepath.Join(ws.root, e.Target)
		if ov, ok := opts.OverridePaths[e.Target]; ok {
			dir = ov
		}
		if !dirExists(dir) {
			continue
		}
		out = append(out, resolvedOverride{alias: e.Alias, target: e.Target, dir: dir})
	}
	// Sort by alias for determinism.
	sort.Slice(out, func(i, j int) bool { return out[i].alias < out[j].alias })
	return out
}

// overrideInputArgs renders a resolved override set as nix flags:
//
//	--override-input <alias> git+file://<target_dir>
func overrideInputArgs(rs []resolvedOverride) []string {
	out := make([]string, 0, 3*len(rs))
	for _, o := range rs {
		out = append(out, "--override-input", o.alias, o.url())
	}
	return out
}

// overriddenInputs renders a resolved override set as the applied-state's record
// of WHAT AN APPLY OVERRODE: workspace repo key -> the local flake URL nix was
// pointed at. The KEY SET is the claim ("these repos were built from a local
// clone, not from the terminal's lock"); the value is diagnostic, and is never
// empty for a present key — under a coordinated-worktree apply it names the set
// member the build actually read, which is not <root>/<name>.
func overriddenInputs(rs []resolvedOverride) map[string]string {
	m := make(map[string]string, len(rs))
	for _, o := range rs {
		m[o.target] = o.url()
	}
	return m
}

// overrideInputArgsFor returns --override-input flags for the given consumer
// repo, using the workspace lock's per-edge aliases. See resolveOverridesForLock
// for the edge selection and overrideInputArgs for the rendering.
func (ws *Workspace) overrideInputArgsFor(consumer string, opts overrideOpts) []string {
	return ws.overrideInputArgsForLock(ws.lock, consumer, opts)
}

// overrideInputArgsForLock is overrideInputArgsFor keyed on an explicit lock
// rather than ws.lock, so callers that need a freshly derived lock (e.g. hook
// fan-out via effectiveLock) get overrides even when ws.lock is empty/stale.
//
// The edge set this walks is the SAME one markApplied maps through to record
// AppliedState.LockedRevs (terminalLockedRevs), which is why an apply's override
// set and its recorded lock evidence always describe the same list of repos: an
// edge here is an alias there, and no edge means neither an override nor a lock
// entry. The two are not identical, though — the override set is the SUBSET whose
// clone exists on disk (see resolveOverridesForLock).
func (ws *Workspace) overrideInputArgsForLock(lk *Lock, consumer string, opts overrideOpts) []string {
	return overrideInputArgs(ws.resolveOverridesForLock(lk, consumer, opts))
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
