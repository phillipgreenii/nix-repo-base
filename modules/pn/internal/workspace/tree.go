package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
)

// TreeOptions configures Tree.
type TreeOptions struct {
	// AllInputs would show all flake inputs, not just workspace-internal deps.
	// Not yet implemented; the renderer always shows workspace-internal deps.
	AllInputs bool
}

const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Tree writes the workspace dependency graph to w, rooted at the terminal
// flake, using the DAG derived from each repo's declared flake inputs. The
// first time a repo appears it is printed normally; a repeat reference is
// dimmed when the output is a color-capable terminal, otherwise prefixed "*".
func (ws *Workspace) Tree(ctx context.Context, w io.Writer, _ TreeOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	_, dependsOn, err := ws.deriveDAG(ctx)
	if err != nil {
		return err
	}
	renderTree(w, terminal, dependsOn, colorEnabled(w))
	return nil
}

// renderTree prints the dependency graph rooted at terminal. dependsOn maps a
// repo to the (alphabetically sorted) workspace repos it depends on. When color
// is true, repeat references are dimmed; otherwise they are prefixed with "*".
func renderTree(w io.Writer, terminal string, dependsOn map[string][]string, color bool) {
	fmt.Fprintln(w, terminal)
	visited := map[string]bool{terminal: true}
	renderChildren(w, terminal, "", dependsOn, visited, color)
}

func renderChildren(w io.Writer, node, prefix string, dependsOn map[string][]string, visited map[string]bool, color bool) {
	children := dependsOn[node]
	for i, child := range children {
		isLast := i == len(children)-1
		connector, childPrefix := "├── ", prefix+"│   "
		if isLast {
			connector, childPrefix = "└── ", prefix+"    "
		}
		if visited[child] {
			// Repeat reference: dim it (color) or mark it with "*" (no color).
			if color {
				fmt.Fprintf(w, "%s%s%s%s%s\n", prefix, connector, ansiDim, child, ansiReset)
			} else {
				fmt.Fprintf(w, "%s%s*%s\n", prefix, connector, child)
			}
			continue
		}
		fmt.Fprintf(w, "%s%s%s\n", prefix, connector, child)
		visited[child] = true
		renderChildren(w, child, childPrefix, dependsOn, visited, color)
	}
}

// colorEnabled reports whether ANSI color should be used for w: true only when
// w is a character device (a terminal) and NO_COLOR is unset. Non-terminal
// writers (files, pipes, test buffers) get plain output.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
