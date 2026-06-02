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

// overrideOpts configures overrideInputArgs.
type overrideOpts struct {
	// ExcludeTerminal omits the terminal repo (build/apply build it, so it must
	// not override itself).
	ExcludeTerminal bool
	// OverridePaths maps repo key -> absolute path, replacing the default clone
	// location for that repo.
	OverridePaths map[string]string
}

// overrideInputArgs returns --override-input flags pinning each declared,
// non-excluded workspace repo whose clone exists on disk to its local clone via
// git+file://. The override NAME is the repo's resolved input-name; the PATH is
// the repo's clone dir (or its --override-path override). Sorted by repo key.
func (ws *Workspace) overrideInputArgs(opts overrideOpts) []string {
	if ws == nil || ws.config == nil {
		return []string{}
	}
	terminal := ws.config.Workspace.Terminal
	names := orderedRepoNames(ws.config.Repos)
	out := make([]string, 0, 3*len(names))
	for _, name := range names {
		if opts.ExcludeTerminal && name == terminal {
			continue
		}
		dir := filepath.Join(ws.root, name)
		if ov, ok := opts.OverridePaths[name]; ok {
			dir = ov
		}
		if !dirExists(dir) {
			continue
		}
		out = append(out, "--override-input", ws.config.InputNameFor(name), "git+file://"+dir)
	}
	return out
}

// dirExists reports whether p exists and is a directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
