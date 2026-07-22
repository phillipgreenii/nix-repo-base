package workspace

import (
	"context"
	"path/filepath"
	"strings"
)

// WorkspaceInfo is the stable JSON contract emitted by `pn workspace info`.
type WorkspaceInfo struct {
	Wsid     string `json:"wsid"`
	Root     string `json:"root"`
	Terminal string `json:"terminal"`
	// WorkforestsDir is the raw configured workforests_dir value (or the
	// ".workforests" default) — see WorkspaceConfig.WorkforestsDirName.
	WorkforestsDir string `json:"workforests_dir"`
	// InWorkforest reports whether Root itself is a coordinated workforest set
	// — see Workspace.inWorkforest.
	InWorkforest bool `json:"in_workforest"`
	// CanonicalRoot is the workspace root outside any set: Root itself when
	// not InWorkforest, or the derived ancestor when InWorkforest — empty when
	// InWorkforest and WorkforestsDir is absolute (undefined; see
	// Workspace.canonicalRoot).
	CanonicalRoot string     `json:"canonical_root"`
	Repos         []RepoInfo `json:"repos"`
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
		Wsid:           ws.config.Workspace.Id,
		Root:           ws.root,
		Terminal:       ws.config.Workspace.Terminal,
		WorkforestsDir: ws.config.WorkforestsDirName(),
		InWorkforest:   ws.inWorkforest(),
		CanonicalRoot:  ws.canonicalRoot(),
	}
	for _, name := range ws.topoAlpha(ctx) {
		// Key the applied-state lookup by the canonical path via the shared
		// helper — the same rule markApplied/needsRebuild use — so an
		// override-path apply's record is found here (pg2-k43p.3).
		path := ws.appliedStateKeyPath(name)
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

// canonicalRoot returns the canonical workspace root. When rooted inside a set
// (<canonical>/<workforests_dir>/<branch>), strip <branch> then the (possibly
// multi-segment, relative) <workforests_dir>. If workforests_dir is absolute,
// the set lives outside any canonical tree, so canonical root is undefined ("").
func (ws *Workspace) canonicalRoot() string {
	if !ws.inWorkforest() {
		return ws.root
	}
	wf := ws.config.WorkforestsDirName()
	if filepath.IsAbs(wf) {
		return ""
	}
	afterBranch := filepath.Dir(ws.root) // strip <branch>
	trimmed := strings.TrimSuffix(afterBranch, string(filepath.Separator)+filepath.Clean(wf))
	if trimmed == afterBranch { // suffix didn't match (unexpected layout) — fall back to one level up
		return filepath.Dir(afterBranch)
	}
	return trimmed
}
