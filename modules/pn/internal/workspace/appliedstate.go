package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// appliedStateSchema is the current on-disk schema version of an applied-state
// record. Schema 2 (ADR 0025) ADDS locked_revs; it does NOT change the meaning of
// any pre-existing field, so no read-time migration is needed and none is done —
// see readAppliedState.
//
// The version exists so a CONSUMER can tell "this record predates locked_revs, so
// there is no lock information" (schema < 2) from "this record carries the apply's
// complete lock information and this repo simply has no entry in it" (schema >= 2,
// key absent). Those two look identical if you probe the map alone, and they are
// NOT the same claim: the second is positive evidence that the repo was not a
// flake input of the terminal, the first is no evidence at all. Distinguishing
// them by version rather than by emptiness is what lets `pb gate check` skip its
// lock condition for OLD records (so gates keep resolving on an un-upgraded pn
// instead of every gate becoming permanently unresolvable) while still applying it
// to every record a current pn writes.
const appliedStateSchema = 2

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
	Dirty      bool              `json:"dirty"`
	AppliedAt  string            `json:"applied_at"`
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
// schema migration: ADR 0025 only ADDED LockedRevs, so every field an older record
// carries still means exactly what it meant when written. A schema-1 record
// therefore reads back as itself — Schema 0, no LockedRevs — and its Schema value
// is left AT 0 rather than stamped forward, because 0 is the honest statement
// "this record carries no lock information" that consumers key their fail-open
// decision on (see appliedStateSchema). Records gain locked_revs on the next
// successful apply.
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
