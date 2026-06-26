package workspace

import (
	"os"
	"testing"
)

func TestAppliedState_RoundTripAtomicPerPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a, b := "/ws/repoA", "/ws/repoB"
	if appliedStateFile(a) == appliedStateFile(b) {
		t.Fatal("distinct paths must map to distinct files")
	}
	if _, ok, _ := readAppliedState(a); ok {
		t.Fatal("expected no state before write")
	}
	st := AppliedState{AppliedRef: "deadbeef", Dirty: false, AppliedAt: "2026-06-26T00:00:00Z"}
	if err := writeAppliedState(a, st); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := readAppliedState(a)
	if err != nil || !ok || got != st {
		t.Fatalf("round-trip: got %+v ok=%v err=%v", got, ok, err)
	}
	// no leftover temp files in the dir
	ents, _ := os.ReadDir(appliedStateDir())
	for _, e := range ents {
		if !hexFilenameRe.MatchString(e.Name()) {
			t.Fatalf("unexpected non-hex (temp?) file left behind: %s", e.Name())
		}
	}
}
