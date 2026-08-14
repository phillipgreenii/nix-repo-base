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
//
// The three lock fields are the published projection of AppliedState.LockedRevs
// (ADR 0025) and Overridden is the projection of AppliedState.OverriddenInputs.
// ADR 0012 declares this schema a stable consumed API in which new OPTIONAL fields
// may be added, which is what these are: nothing was renamed or removed, so an
// existing consumer is unaffected.
type RepoInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// AppliedRef is this checkout's local HEAD at apply time — evidence that an
	// apply RAN. It is NOT evidence that the applied system CONTAINS that commit;
	// for a repo the terminal consumes as a flake input that requires LockedRev.
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
	// AppliedStateSchema is the schema version of the applied-state record this
	// entry was read from; 0 when the record predates locked_revs (or no record
	// exists). A consumer MUST branch on it before trusting TerminalInput, because
	// an old record's `false` means "no information recorded", not "not an input".
	AppliedStateSchema int `json:"applied_state_schema"`
	// TerminalInput reports whether the apply that wrote this record consumed the
	// repo as a flake input of the terminal. Meaningful only when
	// AppliedStateSchema >= 2.
	TerminalInput bool `json:"terminal_input"`
	// LockedRev is the rev the TERMINAL's flake.lock pinned for this repo at that
	// apply. Empty while TerminalInput is true means the apply could not establish
	// it, and a consumer MUST fail closed rather than fall back to AppliedRef.
	//
	// It is what the built system carries ONLY when Overridden is false. When the
	// apply overrode the input, the build read the LOCAL clone at eval-time HEAD and
	// this rev normally TRAILS it.
	LockedRev string `json:"locked_rev"`
	// Overridden reports whether the apply that wrote this record passed
	// `--override-input` for this repo, i.e. built it from a LOCAL clone rather than
	// from the terminal's flake.lock. Meaningful only when AppliedStateSchema >= 3;
	// on an older record it is false because the field did not exist, which is NOT
	// evidence that the repo was lock-built. A consumer MUST branch on the schema
	// before reading it, and MUST NOT test anything against LockedRev when it is
	// true (bead pg2-14yqh).
	//
	// True implies TerminalInput: both derive from the terminal's lock edges, and an
	// override additionally requires the clone to exist on disk.
	Overridden bool `json:"overridden"`
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
			ri.AppliedStateSchema = st.Schema
			// Project this repo's own entry. Presence of the KEY (not a non-empty
			// value) is what makes it a terminal flake input — an entry with an empty
			// rev is the fail-closed state, and collapsing the two would silently
			// turn "the apply cannot say what it built this from" into "no lock check
			// applies here".
			if rev, isInput := st.LockedRevs[name]; isInput {
				ri.TerminalInput = true
				ri.LockedRev = rev
			}
			// Same key-presence rule for the override set: a present key is the claim
			// "this apply built the repo from that local clone", and the URL beside it
			// is diagnostic only, so it is never tested for emptiness here.
			if _, wasOverridden := st.OverriddenInputs[name]; wasOverridden {
				ri.Overridden = true
			}
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
