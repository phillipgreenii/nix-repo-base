package workspace

import (
	"context"
	"path/filepath"
)

// Repo is one discovered workspace repo entry.
type Repo struct {
	Name string
	URL  string
	Path string
	// InputName is the flake input this repo overrides — its resolved
	// input-name (see WorkspaceConfig.InputNameFor). It is empty for the
	// terminal flake, which overrides nothing.
	InputName string
}

// Discover returns the workspace's repos in dependency order: dependencies
// first, the terminal flake last, siblings broken alphabetically. The order is
// derived from the inputs each repo declares in its flake.nix (via deriveDAG),
// the same source of truth as the lock. Each non-terminal repo carries its
// resolved inputName; the terminal repo's InputName is empty.
//
// This is the Go equivalent of pn-discover-workspace, which emitted the same
// topologically-ordered [{path, inputName}] shape.
func (ws *Workspace) Discover(ctx context.Context) ([]Repo, error) {
	order, _, err := ws.deriveDAG(ctx)
	if err != nil {
		return nil, err
	}
	terminal := ws.config.Workspace.Terminal
	out := make([]Repo, 0, len(order))
	for _, name := range order {
		inputName := ""
		if name != terminal {
			inputName = ws.config.InputNameFor(name)
		}
		out = append(out, Repo{
			Name:      name,
			URL:       ws.config.Repos[name].URL,
			Path:      filepath.Join(ws.root, name),
			InputName: inputName,
		})
	}
	return out, nil
}
