package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// TreeOptions configures Tree.
type TreeOptions struct {
	// Terminal overrides workspace.terminal for this tree render.
	Terminal string
	// AllInputs shows every flake input from the terminal's flake.lock, not just
	// workspace-internal deps.
	AllInputs bool
}

const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Tree writes the workspace dependency graph to w, rooted at the terminal
// flake. By default it shows only workspace-internal deps, using the DAG
// derived from each repo's declared flake inputs. With opts.AllInputs it shows
// every flake input read from the terminal's flake.lock graph instead. The
// first time a node appears it is printed normally; a repeat reference is
// dimmed when the output is a color-capable terminal, otherwise prefixed "*".
func (ws *Workspace) Tree(ctx context.Context, w io.Writer, opts TreeOptions) error {
	terminal, err := ws.config.TerminalRepo()
	if err != nil {
		return err
	}
	if opts.AllInputs {
		return ws.treeAllInputs(ctx, w, terminal)
	}
	inputURLs, err := ws.gatherInputURLs(ctx)
	if err != nil {
		return err
	}
	edges, _, err := buildEdges(ws.config.Repos, inputURLs)
	if err != nil {
		return err
	}
	repoKeys := orderedRepoNames(ws.config.Repos)
	dependsOn := edgesToDependsOn(edges, repoKeys)
	if err != nil {
		return err
	}
	renderTree(w, terminal, dependsOn, colorEnabled(w))
	return nil
}

// treeAllInputs renders the full flake-input graph from the terminal flake.lock,
// generating the lock first if it is absent (matching the bash). Workspace repos
// appear under their directory basename; all other inputs keep their lock keys.
func (ws *Workspace) treeAllInputs(ctx context.Context, w io.Writer, terminal string) error {
	terminalDir := filepath.Join(ws.root, terminal)
	lockPath := filepath.Join(terminalDir, "flake.lock")
	if !fileExists(lockPath) {
		fmt.Fprintf(w, "info: generating flake.lock for %s\n", terminal)
		if _, err := ws.runner.Run(ctx, "nix", []string{"flake", "lock", "path:" + terminalDir},
			exec.RunOptions{Stdout: w, Stderr: w}); err != nil {
			return fmt.Errorf("generate flake.lock for %s: %w", terminal, err)
		}
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", lockPath, err)
	}
	root, dependsOn, err := buildAllInputsGraph(data, filepath.Base(terminalDir), ws.workspaceDisplayNamesFromEdges(terminal))
	if err != nil {
		return fmt.Errorf("parse %s: %w", lockPath, err)
	}
	renderTree(w, root, dependsOn, colorEnabled(w))
	return nil
}

// workspaceDisplayNames maps each non-terminal repo's resolved inputName to its
// display name (the repo's directory basename, i.e. its key) so the all-inputs
// tree shows workspace repos by directory rather than by lock input key.
func (ws *Workspace) workspaceDisplayNames(terminal string) map[string]string {
	m := make(map[string]string)
	for _, key := range orderedRepoNames(ws.config.Repos) {
		if key == terminal {
			continue
		}
		m[ws.config.InputNameFor(key)] = key
	}
	return m
}

// buildAllInputsGraph parses a flake.lock and returns the full input dependency
// graph in display-name space, plus the root node's display name. Each lock
// node key is translated via display: the lock's root node becomes
// terminalBasename, a workspace inputName becomes its repo basename (from
// wsDisplay), and any other key is kept as-is. An input value is a direct
// dependency when it is a node-key string or a single-element follow ([X]);
// multi-element sub-input follows ([X,Y,...]) are not direct deps and are
// skipped. Dependency lists are sorted by display name. Nodes with no deps are
// omitted from the map.
func buildAllInputsGraph(lockJSON []byte, terminalBasename string, wsDisplay map[string]string) (string, map[string][]string, error) {
	var lock struct {
		Nodes map[string]struct {
			Inputs map[string]json.RawMessage `json:"inputs"`
		} `json:"nodes"`
		Root string `json:"root"`
	}
	if err := json.Unmarshal(lockJSON, &lock); err != nil {
		return "", nil, err
	}
	rootKey := lock.Root
	if rootKey == "" {
		rootKey = "root"
	}
	display := func(key string) string {
		if key == rootKey {
			return terminalBasename
		}
		if d, ok := wsDisplay[key]; ok {
			return d
		}
		return key
	}

	dependsOn := make(map[string][]string)
	for nodeKey, node := range lock.Nodes {
		var deps []string
		for _, raw := range node.Inputs {
			target, ok := resolveInputTarget(raw)
			if !ok {
				continue
			}
			deps = append(deps, display(target))
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			dependsOn[display(nodeKey)] = deps
		}
	}
	return display(rootKey), dependsOn, nil
}

// resolveInputTarget resolves one flake.lock input value to its target node
// key. A string is a direct dependency; a single-element array ([X]) follows
// node X (also a direct dependency); a multi-element array is a sub-input
// follow and is not a direct dependency. Returns ("", false) when there is no
// direct-dependency target.
func resolveInputTarget(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) == 1 {
		return arr[0], true
	}
	return "", false
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
