package workspace

import (
	"context"
	"fmt"
	"io"
)

// TreeOptions configures Tree.
type TreeOptions struct {
	// AllInputs, if true, shows all flake inputs, not just workspace-internal
	// deps. Currently unused — see TODO below.
	AllInputs bool
}

// Tree writes a simplified workspace tree (one repo per line, plus the locked
// rev) to w.
//
// TODO(tc-perh.5): port the full ASCII dep-graph renderer from
// pn-workspace-tree.sh — read terminal flake's flake.lock and recursively
// render the dependency graph with └── / ├── connectors and visited-node
// dedup. The current implementation prints a flat list of locked repos and
// their revs, which is sufficient as a starting point.
func (ws *Workspace) Tree(ctx context.Context, w io.Writer, opts TreeOptions) error {
	fmt.Fprintf(w, "%s\n", ws.config.Workspace.Name)
	names := orderedRepoNames(ws.config.Repos)
	for i, name := range names {
		connector := "├── "
		if i == len(names)-1 {
			connector = "└── "
		}
		r := ws.config.Repos[name]
		if locked, ok := ws.lock.Repos[name]; ok && locked.Rev != "" {
			fmt.Fprintf(w, "%s%s (%s)\n", connector, name, locked.Rev)
		} else {
			fmt.Fprintf(w, "%s%s [%s]\n", connector, name, r.URL)
		}
	}
	return nil
}
