package store

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// AuditOptions configures Audit.
type AuditOptions struct {
	// Full requests the dead-paths estimate (slow, requires sudo). Maps to
	// the bash `--full` flag.
	Full bool
}

// Audit reports profile generations and Nix store size.
//
// TODO(tc-perh.6): port full pn-store-audit.sh semantics: profile discovery
// (system / home-manager / user / devbox-global / devbox-projects), per-profile
// generation listing via `nix-env --list-generations`, per-profile closure size
// via `nix path-info -S`, and section-header formatted output. The current
// implementation runs the minimal store-level subprocesses sufficient for
// unit-test scaffolding; integration tests (Task 14) are expected to flag
// behavioral gaps.
func (s *Store) Audit(ctx context.Context, opts AuditOptions) error {
	// Store size — the bash uses `du -sh /nix/store` (gated on platform).
	if _, err := s.runner.Run(ctx, "du", []string{"-sh", "/nix/store"}, exec.RunOptions{}); err != nil {
		return fmt.Errorf("store size: %w", err)
	}
	if opts.Full {
		// Reclaimable estimate — bash invokes `nix store gc --dry-run`.
		if _, err := s.runner.Run(ctx, "nix", []string{"store", "gc", "--dry-run"}, exec.RunOptions{}); err != nil {
			return fmt.Errorf("dead paths estimate: %w", err)
		}
	}
	return nil
}
