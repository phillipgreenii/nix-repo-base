//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScenarioRequiresNix verifies the marker-file detection: a scenario dir
// containing a "requires-nix" marker file is reported as requiring nix; one
// without the marker is not. This is the generic capability gate that lets
// scenarios whose setup.sh or command invokes `nix build`/`nix fmt`
// (e.g. S23) self-skip when nix is unavailable instead of hard-failing.
func TestScenarioRequiresNix(t *testing.T) {
	t.Run("marker present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "requires-nix"), nil, 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if !scenarioRequiresNix(dir) {
			t.Errorf("scenarioRequiresNix(%q) = false, want true (marker present)", dir)
		}
	})

	t.Run("marker absent", func(t *testing.T) {
		dir := t.TempDir()
		if scenarioRequiresNix(dir) {
			t.Errorf("scenarioRequiresNix(%q) = true, want false (no marker)", dir)
		}
	})
}

// TestS23DeclaresRequiresNix verifies the real S23 scenario directory carries
// the requires-nix marker, so the runScenario gate skips it (rather than
// hard-failing in setup.sh) when nix is unavailable.
func TestS23DeclaresRequiresNix(t *testing.T) {
	scenarioDir := filepath.Join("scenarios", "s23-happy-path-format")
	if !scenarioRequiresNix(scenarioDir) {
		t.Errorf("S23 scenario %q is missing the requires-nix marker; runScenario cannot skip it when nix is unavailable", scenarioDir)
	}
}

// TestNixSkipGate proves the skip decision: a scenario that requires nix MUST
// be skipped (t.Skip, not t.Fatal) when nix is unavailable, and MUST proceed
// when nix is available. nixAvailable is stubbed so the test does not depend on
// whether nix is actually installed.
func TestNixSkipGate(t *testing.T) {
	scenarioDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scenarioDir, "requires-nix"), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	orig := nixAvailable
	t.Cleanup(func() { nixAvailable = orig })

	// skipScenarioIfNixUnavailable calls t.Skip when the scenario requires nix
	// and nix is unavailable. t.Skip aborts the goroutine via runtime.Goexit,
	// so any code AFTER the gate is unreachable on a skip. We detect that by
	// flipping `reachedPastGate` immediately after the call: it stays false on a
	// skip and becomes true when the gate lets execution proceed. We also assert
	// the subtest's Skipped()/Failed() status to be unambiguous.
	t.Run("skips when nix unavailable", func(t *testing.T) {
		nixAvailable = func() bool { return false }
		reachedPastGate := false
		var innerSkipped, innerFailed bool
		t.Run("inner", func(it *testing.T) {
			defer func() {
				innerSkipped = it.Skipped()
				innerFailed = it.Failed()
			}()
			skipScenarioIfNixUnavailable(it, scenarioDir)
			reachedPastGate = true
		})
		if reachedPastGate {
			t.Errorf("gate did not skip: execution proceeded past the gate when nix unavailable")
		}
		if !innerSkipped {
			t.Errorf("inner test was not skipped (Skipped()=false) when nix unavailable")
		}
		if innerFailed {
			t.Errorf("inner test failed instead of skipping when nix unavailable")
		}
	})

	t.Run("proceeds when nix available", func(t *testing.T) {
		nixAvailable = func() bool { return true }
		reachedPastGate := false
		t.Run("inner", func(it *testing.T) {
			skipScenarioIfNixUnavailable(it, scenarioDir)
			reachedPastGate = true
		})
		if !reachedPastGate {
			t.Errorf("gate skipped despite nix being available")
		}
	})
}
