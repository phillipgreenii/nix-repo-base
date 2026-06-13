package workspace

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// terminalRequiredError returns the standard error for required commands when
// no terminal can be resolved. The candidates slice is the set of repo names
// that autoDetectTerminal would accept; it is included in the message as a
// hint so the user knows which name to set.
func terminalRequiredError(candidates []string) error {
	var candidateStr string
	if len(candidates) == 0 {
		candidateStr = "none detected; check that at least one repo has a flake.nix"
	} else {
		sort.Strings(candidates)
		candidateStr = strings.Join(candidates, ", ")
	}
	return fmt.Errorf(
		"no terminal repo configured for this workspace.\n  set workspace.terminal in pn-workspace.toml to one of: %s,\n  or pass --terminal <name> to this command.",
		candidateStr,
	)
}

const terminalWarningMessage = "warning: no terminal configured. Commands like build/apply/update will not work until set. Set workspace.terminal in pn-workspace.toml or pass --terminal <name>."

// detectTerminalCandidates returns the repo keys that could serve as
// terminal — i.e., repos with a non-empty flake_path that are sinks (no
// other workspace repo consumes them). Uses the workspace lock when available.
func (ws *Workspace) detectTerminalCandidates(ctx context.Context) []string {
	// Use effectiveLock to get the edge set for sink detection.
	lock, _, err := ws.effectiveLock(ctx)
	if err != nil || lock == nil {
		// Fallback: return all repos with a known flake_path.
		return ws.reposWithFlakePath()
	}

	// Build inbound-edge count from lock edges (among repos in config).
	inbound := make(map[string]int, len(ws.config.Repos))
	for k := range ws.config.Repos {
		inbound[k] = 0
	}
	for _, e := range lock.Edges {
		if _, ok := inbound[e.Target]; ok {
			inbound[e.Target]++
		}
	}

	// Candidates: repos with zero inbound edges and a flake_path.
	var candidates []string
	for key, count := range inbound {
		if count > 0 {
			continue
		}
		fp := ws.resolveFlakePath(key)
		if fp != "" {
			candidates = append(candidates, key)
		}
	}
	sort.Strings(candidates)
	return candidates
}

// reposWithFlakePath returns repo keys that have a resolvable flake path.
func (ws *Workspace) reposWithFlakePath() []string {
	var out []string
	for key := range ws.config.Repos {
		if ws.resolveFlakePath(key) != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// requireTerminal resolves the effective terminal for required commands. When
// flagTerminal is non-empty it is used directly (no candidate check). When
// empty and the config has no terminal, returns terminalRequiredError with the
// auto-detected candidate list.
func (ws *Workspace) requireTerminal(ctx context.Context, flagTerminal string) (string, error) {
	if flagTerminal != "" {
		return flagTerminal, nil
	}
	t := ws.config.Workspace.Terminal
	if t == "" {
		candidates := ws.detectTerminalCandidates(ctx)
		return "", terminalRequiredError(candidates)
	}
	return t, nil
}
