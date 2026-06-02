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
