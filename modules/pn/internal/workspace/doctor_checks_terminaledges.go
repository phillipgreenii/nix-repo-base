// internal/workspace/doctor_checks_terminaledges.go
package workspace

import (
	"context"
	"fmt"
	"path/filepath"
)

const terminalEdgesCheckID = "terminal-has-edges"

// checkTerminalEdges flags a terminal repo that has zero outgoing edges while
// the workspace holds other repos. That shape usually means the terminal's flake
// inputs point at a mirror (e.g. a Forgejo pull-mirror) of the sibling repos
// that pn-workspace.toml declares under a different URL: no edge is derived, so
// build/apply emit no --override-input and the terminal builds against its
// flake.lock pins instead of the local clones (bead tc-31llk). Silent for
// single-repo workspaces and when no terminal is resolved (terminal-resolvable
// already reports that).
func (ws *Workspace) checkTerminalEdges(_ context.Context, env *doctorEnv) []Finding {
	if env.terminal == "" || env.lock == nil || len(ws.config.Repos) < 2 {
		return nil
	}
	if _, ok := ws.config.Repos[env.terminal]; !ok {
		return nil
	}
	if !isGitRepo(filepath.Join(ws.root, env.terminal)) {
		return nil
	}
	for _, e := range env.lock.Edges {
		if e.Consumer == env.terminal {
			return nil
		}
	}
	return []Finding{{
		CheckID: terminalEdgesCheckID, Repo: env.terminal, Severity: SevError,
		Message: fmt.Sprintf("terminal %q has no edges to any other workspace repo: its flake inputs match none of the workspace repo URLs, so apply/build inject no --override-input and it builds against its flake.lock pins", env.terminal),
		Manual:  fmt.Sprintf("if %q consumes sibling repos through a mirror URL (e.g. a Forgejo pull-mirror), add that URL to the sibling's mirror_urls in pn-workspace.toml ([repos.<name>] mirror_urls = [\"...\"]), then run pn workspace lock", env.terminal),
	}}
}
