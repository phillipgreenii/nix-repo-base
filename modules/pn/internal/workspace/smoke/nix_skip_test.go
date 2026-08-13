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

// --- network capability gate (mirrors the nix gate above) ---

// TestScenarioRequiresNetwork verifies the marker-file detection for the
// requires-network capability: a scenario dir containing the marker is reported
// as requiring network; one without it is not.
func TestScenarioRequiresNetwork(t *testing.T) {
	t.Run("marker present", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "requires-network"), nil, 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if !scenarioRequiresNetwork(dir) {
			t.Errorf("scenarioRequiresNetwork(%q) = false, want true (marker present)", dir)
		}
	})

	t.Run("marker absent", func(t *testing.T) {
		dir := t.TempDir()
		if scenarioRequiresNetwork(dir) {
			t.Errorf("scenarioRequiresNetwork(%q) = true, want false (no marker)", dir)
		}
	})
}

// TestS23DeclaresRequiresNetwork verifies the real S23 scenario directory carries
// the requires-network marker. S23 is the ONLY scenario that must resolve an
// external flake input (`nixpkgs`, for `nix fmt`), and the pn-smoke-tests flake
// check runs with a working nix but no network — so without this marker S23
// hard-fails there with an opaque SSL/CA error instead of skipping.
func TestS23DeclaresRequiresNetwork(t *testing.T) {
	scenarioDir := filepath.Join("scenarios", "s23-happy-path-format")
	if !scenarioRequiresNetwork(scenarioDir) {
		t.Errorf("S23 scenario %q is missing the requires-network marker; runScenario cannot skip it when network is unavailable", scenarioDir)
	}
}

// TestNetworkSkipGate proves the skip decision, mirroring TestNixSkipGate: a
// scenario that requires network MUST be skipped (t.Skip, not t.Fatal) when
// network is unavailable, and MUST proceed when it is available.
// networkAvailable is stubbed so the test does not depend on real connectivity.
func TestNetworkSkipGate(t *testing.T) {
	scenarioDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(scenarioDir, "requires-network"), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	orig := networkAvailable
	t.Cleanup(func() { networkAvailable = orig })

	// t.Skip aborts the goroutine via runtime.Goexit, so code AFTER the gate is
	// unreachable on a skip. reachedPastGate detects that; the Skipped()/Failed()
	// assertions make the outcome unambiguous.
	t.Run("skips when network unavailable", func(t *testing.T) {
		networkAvailable = func() bool { return false }
		reachedPastGate := false
		var innerSkipped, innerFailed bool
		t.Run("inner", func(it *testing.T) {
			defer func() {
				innerSkipped = it.Skipped()
				innerFailed = it.Failed()
			}()
			skipScenarioIfNetworkUnavailable(it, scenarioDir)
			reachedPastGate = true
		})
		if reachedPastGate {
			t.Errorf("gate did not skip: execution proceeded past the gate when network unavailable")
		}
		if !innerSkipped {
			t.Errorf("inner test was not skipped (Skipped()=false) when network unavailable")
		}
		if innerFailed {
			t.Errorf("inner test failed instead of skipping when network unavailable")
		}
	})

	t.Run("proceeds when network available", func(t *testing.T) {
		networkAvailable = func() bool { return true }
		reachedPastGate := false
		t.Run("inner", func(it *testing.T) {
			skipScenarioIfNetworkUnavailable(it, scenarioDir)
			reachedPastGate = true
		})
		if !reachedPastGate {
			t.Errorf("gate skipped despite network being available")
		}
	})
}

// TestNetworkAvailableProbeUsesNixBuildTop pins the probe's mechanism: inside a
// nix builder (NIX_BUILD_TOP set) there is no network by construction, so the
// default probe MUST report unavailable; with it unset it MUST report available.
// This is what makes S23 skip in the pn-smoke-tests check and run for a developer.
func TestNetworkAvailableProbeUsesNixBuildTop(t *testing.T) {
	t.Setenv("NIX_BUILD_TOP", "/build")
	if networkAvailable() {
		t.Errorf("networkAvailable() = true with NIX_BUILD_TOP set; want false (nix build sandbox has no network)")
	}
	// Empty is equivalent to unset for this probe, and t.Setenv restores the
	// original value on cleanup — which matters because this very suite runs
	// inside a nix builder, where NIX_BUILD_TOP is genuinely set.
	t.Setenv("NIX_BUILD_TOP", "")
	if !networkAvailable() {
		t.Errorf("networkAvailable() = false with NIX_BUILD_TOP unset; want true")
	}
}
