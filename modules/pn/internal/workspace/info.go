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
// (<canonical>/<workforests_dir>/<branch>), strip the <branch> (which may be
// slashed → multiple segments) and the (possibly multi-segment, relative)
// <workforests_dir>. If workforests_dir is absolute, the set lives outside any
// canonical tree, so canonical root is undefined (""). See workforestLocation.
func (ws *Workspace) canonicalRoot() string {
	canonical, in := ws.workforestLocation()
	if !in {
		return ws.root
	}
	return canonical
}

// workforestLocation classifies ws.root relative to the configured workforests
// dir. It reports whether ws.root lives inside the workforests subtree (i.e. it
// is a coordinated workforest set root) and, when it does under a RELATIVE
// workforests_dir, the derived canonical workspace root (the ancestor that holds
// the workforests dir). For an ABSOLUTE workforests_dir the set lives outside any
// canonical tree, so canonical is "" (undefined) — the documented M1 behaviour.
// The canonical return is meaningful only when in==true; callers use ws.root for
// the not-in case.
//
// Detection is structural and tolerant of a NESTED set dir. A set created from a
// SLASHED branch name (the design's `wf/<bead-id>-<slug>` convention) lives at
// <workforests_dir>/<a>/<b>/… — MORE than one path segment below the workforests
// dir. Rather than assume exactly one segment between the set root and the
// workforests dir, we walk ws.root's ancestors and match the first (deepest)
// ancestor whose trailing path segments equal the configured workforests_dir; its
// prefix (workforests_dir stripped) is the canonical root. Deepest-match is also
// the safe tie-break: a canonical root whose OWN path happens to contain the
// workforests_dir name resolves to the real (deeper) workforests dir rather than
// the coincidental shallower ancestor. (bead pg2-u1ubb)
func (ws *Workspace) workforestLocation() (canonical string, in bool) {
	root := filepath.Clean(ws.root)
	wf := ws.config.WorkforestsDirName()

	if filepath.IsAbs(wf) {
		wfClean := filepath.Clean(wf)
		// Inside the set iff root is strictly below the absolute workforests dir.
		if root != wfClean && strings.HasPrefix(root, wfClean+string(filepath.Separator)) {
			return "", true // canonical undefined for an absolute workforests_dir
		}
		return "", false
	}

	// Relative workforests_dir: the workforests dir is <canonical>/<wf>. Walk up
	// from ws.root's parent — the set root itself can never BE the workforests dir
	// (a set always lives strictly below it) — and match the first ancestor ending
	// in the wf path segments. The leading separator in the suffix keeps the match
	// on a path-segment boundary (no partial-segment false positives).
	suffix := string(filepath.Separator) + filepath.Clean(wf)
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if strings.HasSuffix(dir, suffix) {
			return strings.TrimSuffix(dir, suffix), true
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", false // reached the filesystem root without a match
		}
	}
}
