package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// appliedStateSchema is the current on-disk schema version of an applied-state
// record. Every bump so far has been PURELY ADDITIVE — schema 2 (ADR 0025) added
// locked_revs, schema 3 (ADR 0025's "what the apply overrode" amendment) added
// overridden_inputs — and neither changed the meaning of any pre-existing field,
// so no read-time migration is needed and none is done (see readAppliedState).
//
// The version exists so a CONSUMER can tell "this record predates the field, so
// there is no information" from "this record carries the apply's complete
// information and this repo simply has no entry in it". Those two look identical
// if you probe the map alone, and they are NOT the same claim: the second is
// positive evidence, the first is no evidence at all. Both maps need that
// distinction, and each consumer decides which way its own absence leans:
//
//   - locked_revs / schema < 2 — `pb gate check` SKIPS its lock condition, so
//     gates keep resolving against an un-upgraded pn rather than every gate
//     becoming permanently unresolvable (a bootstrap stall, since the fix itself
//     ships through an apply).
//   - overridden_inputs / schema < 3 — `pb gate check` reads the absence as NOT
//     overridden and therefore ENFORCES its lock condition, i.e. fail-CLOSED. The
//     asymmetry is deliberate: at schema 2 the lock condition is still fully
//     evaluable, so leaning the other way would ASSERT an override the record does
//     not evidence, and a false "the change shipped" is the expensive direction
//     (agent-support ADR 0046's amendment carries the reasoning).
const appliedStateSchema = 3

type AppliedState struct {
	// Schema is this record's on-disk schema version. Absent (0) means the
	// pre-ADR-0025 layout, which carried no LockedRevs. Never remapped on read.
	Schema int `json:"schema,omitempty"`
	// AppliedRef is the repo's local `git rev-parse HEAD` at apply time — the
	// evidence that an apply actually RAN over this checkout. Its meaning is
	// unchanged by ADR 0025: LockedRevs is a SEPARATE, ADDITIONAL field, so the
	// rebuild-skip gate (needsRebuild) and every existing consumer keep working
	// exactly as before.
	AppliedRef string `json:"applied_ref"`
	// LockedRevs is what the TERMINAL's flake.lock pinned, AT THIS APPLY, for each
	// workspace repo the terminal consumes as a flake input. It is recorded WITH
	// the apply, never read fresh at query time: a later relock must not
	// retroactively make an earlier apply look like it built newer code (the
	// ordering hole ADR 0025 closes).
	//
	// Read the KEY SET, not just the values — the three states are distinct:
	//
	//   - key ABSENT   → the terminal does not consume this repo as a flake input
	//     at all (the terminal itself, or a workspace repo no terminal input
	//     names). No lock claim is possible or needed.
	//   - key present, NON-EMPTY value → the rev the terminal's flake.lock pinned
	//     for it. This is the rev a consumer must test the gated commit against.
	//   - key present, EMPTY value → it IS a terminal flake input but the rev could
	//     not be established (unreadable/absent flake.lock, a follows-only or
	//     path: input). Consumers MUST fail CLOSED on this: it is the one state in
	//     which the apply cannot say what it built that input from.
	LockedRevs map[string]string `json:"locked_revs,omitempty"`
	// OverriddenInputs is WHAT THIS APPLY OVERRODE: for each workspace repo the
	// apply passed `--override-input <alias> git+file://<dir>` for, the local flake
	// URL it pointed nix at. Recorded because the apply is the only moment the fact
	// exists — `Apply` derives the override set from the lock edges AND from which
	// clones are present on disk, and neither the lock nor the record's other
	// fields can reconstruct that later.
	//
	// Read the KEY SET, not the values:
	//
	//   - key ABSENT   → this apply did NOT override the repo, so nix resolved it
	//     from the terminal's flake.lock and LockedRevs[repo] is what the build
	//     carries. Absent because there is no lock edge (the terminal itself, or a
	//     non-input) or because the clone was missing.
	//   - key PRESENT  → the build read the repo from that LOCAL directory at
	//     eval-time HEAD, NOT from LockedRevs[repo] — which normally TRAILS it. A
	//     consumer MUST NOT test anything against LockedRevs[repo] for such a repo
	//     (bead pg2-14yqh; agent-support ADR 0046's amendment).
	//
	// The value is never empty for a present key, so — unlike LockedRevs — there is
	// no third fail-closed state. It is diagnostic: under a coordinated-worktree
	// apply it names the set member the build actually read, which is not
	// <root>/<name>.
	//
	// The key set is a SUBSET of LockedRevs': both come from the terminal's lock
	// edges, and an override additionally requires the clone to exist.
	OverriddenInputs map[string]string `json:"overridden_inputs,omitempty"`
	Dirty            bool              `json:"dirty"`
	AppliedAt        string            `json:"applied_at"`
}

func appliedStateDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "pn-workspace", "applied")
}

func appliedStateFile(repoDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoDir)))
	return filepath.Join(appliedStateDir(), fmt.Sprintf("%x", sum))
}

func writeAppliedState(repoDir string, st AppliedState) error {
	if err := os.MkdirAll(appliedStateDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	final := appliedStateFile(repoDir)
	tmp, err := os.CreateTemp(appliedStateDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}

// readAppliedState loads a repo's applied-state record. There is deliberately NO
// schema migration: every bump so far only ADDED a field (LockedRevs at 2,
// OverriddenInputs at 3), so every field an older record carries still means
// exactly what it meant when written. An older record therefore reads back as
// ITSELF — a schema-1 record as Schema 0 with neither map, a schema-2 record as
// Schema 2 with no OverriddenInputs — and its Schema value is left as written
// rather than stamped forward, because the recorded version is the honest
// statement of which facts this record can speak to, and it is what consumers key
// their compatibility branch on (see appliedStateSchema). Records gain the newer
// fields on the next successful apply.
func readAppliedState(repoDir string) (AppliedState, bool, error) {
	var st AppliedState
	data, err := os.ReadFile(appliedStateFile(repoDir))
	if os.IsNotExist(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}
