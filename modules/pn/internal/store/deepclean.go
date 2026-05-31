package store

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// DeepCleanOptions configures DeepClean.
type DeepCleanOptions struct {
	// DryRun shows what would be cleaned without deleting. Maps to `--dry-run`.
	DryRun bool
	// KeepSince overrides the time-based retention period (e.g. "14d", "2w").
	// Empty falls back to the config default.
	KeepSince string
	// Keep overrides the count-based retention. Negative means "use config default".
	// 0 disables count protection (only current generation is always kept).
	Keep int
}

// DeepClean prunes old profile generations, stale GC roots, result symlinks,
// NH temp roots, and (in non-dry-run mode) runs `nix-store --gc`.
//
// TODO(tc-perh.6): port full pn-store-deepclean.sh semantics: profile
// discovery, generation-pruning via `nix-env --delete-generations`, stale
// nix-profile entries, NH temp roots, result symlinks, runtime-roots summary.
// The current implementation captures the authoritative GC step so callers
// can verify the subprocess seam end-to-end; integration tests (Task 14)
// drive parity with the bash.
func (s *Store) DeepClean(ctx context.Context, opts DeepCleanOptions) error {
	if opts.DryRun {
		// Dry-run produces the reclaimable estimate but does not GC.
		if _, err := s.runner.Run(ctx, "nix", []string{"store", "gc", "--dry-run"}, exec.RunOptions{}); err != nil {
			return fmt.Errorf("dead paths estimate: %w", err)
		}
		return nil
	}
	// Non-dry-run: GC the store. Profile-generation deletes are part of the
	// deferred port (see TODO above).
	if _, err := s.runner.Run(ctx, "sudo", []string{"nix-store", "--gc"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("nix-store --gc: %w", err)
	}
	return nil
}
