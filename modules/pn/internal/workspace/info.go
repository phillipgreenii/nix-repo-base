package workspace

import (
	"context"
	"path/filepath"
)

// WorkspaceInfo is the stable JSON contract emitted by `pn workspace info`.
type WorkspaceInfo struct {
	Wsid     string     `json:"wsid"`
	Root     string     `json:"root"`
	Terminal string     `json:"terminal"`
	Repos    []RepoInfo `json:"repos"`
}

// RepoInfo is one repo's identity + applied state.
type RepoInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
}

// Info joins the configured repos with their per-repo applied-state records.
// It uses the topoAlpha (no-nix-eval) iteration order, never Discover.
func (ws *Workspace) Info(ctx context.Context) (WorkspaceInfo, error) {
	info := WorkspaceInfo{
		Wsid:     ws.config.Workspace.Id,
		Root:     ws.root,
		Terminal: ws.config.Workspace.Terminal,
	}
	for _, name := range ws.topoAlpha(ctx) {
		path := filepath.Join(ws.root, name)
		ri := RepoInfo{Name: name, Path: path}
		if st, ok, err := readAppliedState(path); err != nil {
			return WorkspaceInfo{}, err
		} else if ok {
			ri.AppliedRef = st.AppliedRef
			ri.Dirty = st.Dirty
		}
		info.Repos = append(info.Repos, ri)
	}
	return info, nil
}
