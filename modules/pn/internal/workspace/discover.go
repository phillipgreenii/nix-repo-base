package workspace

import (
	"context"
	"path/filepath"
	"sync"
)

// Repo is one workspace repo entry as surfaced by Discover.
type Repo struct {
	Name       string
	URL        string // display URL (origin in the multi-remote form)
	Path       string
	IsTerminal bool
}

// DiscoverOptions configures Discover.
type DiscoverOptions struct {
	// Terminal overrides workspace.terminal for this invocation.
	Terminal string
}

// Discover returns the workspace's repos in topological order (dependencies
// first, terminal last). Each repo is annotated with IsTerminal.
//
// Discover performs per-repo subprocess fan-out (nix eval + git remote -v)
// in parallel via the workspace's worker pool. Per-repo failures are tolerated
// (the repo simply contributes no out-edges); errors that prevent graph
// construction (slug conflicts, terminal ambiguity, cycles) are returned.
func (ws *Workspace) Discover(opts DiscoverOptions) ([]Repo, error) {
	ctx := context.Background()
	names := orderedRepoNames(ws.config.Repos)
	repoInputs := make(map[string]map[string]string, len(names))
	gitRemotesByRepo := make(map[string]map[string]string, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range names {
		n := n
		repoDir := filepath.Join(ws.root, n)
		wg.Add(1)
		ws.pool.Submit(func() {
			defer wg.Done()
			inputs, _ := readFlakeInputs(ctx, ws.runner, repoDir)
			remotes, _ := readGitRemotes(ctx, ws.runner, repoDir)
			mu.Lock()
			repoInputs[n] = inputs
			gitRemotesByRepo[n] = remotes
			mu.Unlock()
		})
	}
	wg.Wait()

	// Slug-set agreement check (per repo, sequential — fast in-memory).
	for _, n := range names {
		if err := checkRemoteAgreement(n, ws.config.Repos[n], gitRemotesByRepo[n]); err != nil {
			return nil, err
		}
	}

	g, err := buildGraph(ws.config, repoInputs)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return []Repo{}, nil
	}
	terminal, err := selectTerminal(ws.config, g, opts.Terminal)
	if err != nil {
		return nil, err
	}
	order, err := topoSort(g)
	if err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(order))
	for _, name := range order {
		out = append(out, Repo{
			Name:       name,
			URL:        displayURL(ws.config.Repos[name]),
			Path:       filepath.Join(ws.root, name),
			IsTerminal: name == terminal,
		})
	}
	return out, nil
}
