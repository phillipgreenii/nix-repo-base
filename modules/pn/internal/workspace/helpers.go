package workspace

import (
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

// computeOverrideArgs returns the --override-input flags pinning each locked
// workspace repo to its local clone under ws.root. Flags are emitted in
// alphabetical order by repo directory name for deterministic command
// construction. Returns an empty slice when the lock has no entries.
//
// The lock — not the config — is the authoritative source of which repos
// have local clones to override: a repo can be declared in pn-workspace.toml
// before its clone has been materialized (e.g., before `pn init`), in which
// case there is no on-disk directory to point --override-input at.
//
// The override NAME is the repo's configured input-name (config.InputNameFor),
// which may differ from the directory name; the override PATH is always the
// on-disk directory. Repos whose input-name matches the directory — and the
// terminal/leaf flake whose self-override no nix flake declares (nix silently
// ignores unknown --override-input names) — need no special handling.
func computeOverrideArgs(ws *Workspace) []string {
	if ws == nil || ws.lock == nil || len(ws.lock.Repos) == 0 {
		return []string{}
	}
	names := make([]string, 0, len(ws.lock.Repos))
	for n := range ws.lock.Repos {
		names = append(names, n)
	}
	sort.Strings(names)

	overrides := make([]string, 0, 3*len(names))
	for _, name := range names {
		repoDir := filepath.Join(ws.root, name)
		inputName := ws.config.InputNameFor(name)
		overrides = append(overrides, "--override-input", inputName, "path:"+repoDir)
	}
	return overrides
}
