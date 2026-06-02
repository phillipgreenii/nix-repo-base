package workspace

import (
	"context"
	"fmt"
	"io"
)

// TreeOptions configures Tree.
type TreeOptions struct {
	// AllInputs would show all flake inputs, not just workspace-internal deps.
	// Not yet implemented; the renderer always shows workspace-internal deps.
	AllInputs bool
}

// Tree writes the workspace dependency graph to w, rooted at the terminal
// flake, using the DAG derived from each repo's declared flake inputs. A
// dependency reached more than once is rendered in full on first sight and
// thereafter marked "[↑ shown above]".
func (ws *Workspace) Tree(ctx context.Context, w io.Writer, _ TreeOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	_, dependsOn, err := ws.deriveDAG(ctx)
	if err != nil {
		return err
	}
	renderTree(w, terminal, dependsOn)
	return nil
}

// renderTree prints the dependency graph rooted at terminal. dependsOn maps a
// repo to the (alphabetically sorted) workspace repos it depends on.
func renderTree(w io.Writer, terminal string, dependsOn map[string][]string) {
	fmt.Fprintln(w, terminal)
	visited := map[string]bool{terminal: true}
	renderChildren(w, terminal, "", dependsOn, visited)
}

func renderChildren(w io.Writer, node, prefix string, dependsOn map[string][]string, visited map[string]bool) {
	children := dependsOn[node]
	for i, child := range children {
		isLast := i == len(children)-1
		connector, childPrefix := "├── ", prefix+"│   "
		if isLast {
			connector, childPrefix = "└── ", prefix+"    "
		}
		if visited[child] {
			fmt.Fprintf(w, "%s%s%s [↑ shown above]\n", prefix, connector, child)
			continue
		}
		fmt.Fprintf(w, "%s%s%s\n", prefix, connector, child)
		visited[child] = true
		renderChildren(w, child, childPrefix, dependsOn, visited)
	}
}
