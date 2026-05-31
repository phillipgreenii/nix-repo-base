package workspace

import "path/filepath"

// Repo is one discovered workspace repo entry.
type Repo struct {
	Name string
	URL  string
	Path string
}

// Discover returns the workspace's repos in alphabetical order. It is the
// pure-data equivalent of the bash pn-discover-workspace command (without the
// per-repo nix-eval / topological-sort logic, which the Go implementation
// derives from the TOML and lock instead).
//
// TODO(tc-perh.5): port the full topological-sort + inputName resolution
// from pn-discover-workspace.sh. The current implementation returns the
// repos in alphabetical order, not dependency order.
func (ws *Workspace) Discover() []Repo {
	names := orderedRepoNames(ws.config.Repos)
	out := make([]Repo, 0, len(names))
	for _, name := range names {
		out = append(out, Repo{
			Name: name,
			URL:  ws.config.Repos[name].URL,
			Path: filepath.Join(ws.root, name),
		})
	}
	return out
}
