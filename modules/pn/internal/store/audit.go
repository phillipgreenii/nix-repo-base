package store

import (
	"context"
	"fmt"
	"io"
	"os"
)

// AuditOptions configures Audit.
type AuditOptions struct {
	// Full requests the dead-paths estimate (slow, requires sudo). Maps to
	// the bash `--full` flag.
	Full bool
}

// Audit reports profile generations and Nix store size. It writes a
// sectioned report to out (one section per profile category, then the Nix
// Store section). Warnings from devboxProjects (missing search dirs) go to
// errOut. Errors from individual subprocesses are tolerated — a profile that
// errors still prints its header; closure size becomes "unknown". Audit
// returns nil except on a true programming error.
func (s *Store) Audit(ctx context.Context, out, errOut io.Writer, opts AuditOptions) error {
	cfg := LoadConfig(s.env)

	// ─── System Profiles ───────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== System Profiles ===")
	s.auditProfile(ctx, out, "system", s.systemProfile(), true)

	// ─── Home Manager ──────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Home Manager ===")
	hm := s.homeManagerProfile()
	if _, err := os.Lstat(hm); err == nil {
		s.auditProfile(ctx, out, "home-manager", hm, false)
	} else {
		fmt.Fprintln(out, "  (not installed)")
		fmt.Fprintln(out)
	}

	// ─── User Profiles ─────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== User Profiles ===")
	for _, p := range s.userProfiles() {
		s.auditProfile(ctx, out, "user-profiles", p, false)
	}

	// ─── Devbox Global ─────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Devbox Global ===")
	dbg := s.devboxGlobalProfile()
	if dbg != "" {
		s.auditProfile(ctx, out, "devbox-global", dbg, false)
	} else {
		fmt.Fprintln(out, "  (not installed)")
		fmt.Fprintln(out)
	}

	// ─── Devbox Projects ───────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Devbox Projects ===")
	if len(cfg.SearchDirs) == 0 {
		fmt.Fprintln(out, "  (no search dirs configured)")
		fmt.Fprintln(out)
	} else {
		for _, p := range s.devboxProjects(ctx, errOut, cfg.SearchDirs) {
			s.auditProfile(ctx, out, "devbox-projects", p, false)
		}
	}

	// ─── Nix Store ─────────────────────────────────────────────────────────────
	fmt.Fprintln(out, "=== Nix Store ===")
	fmt.Fprintf(out, "Volume used: %s\n", storeSize(ctx, s.runner))
	if opts.Full {
		// nonInteractive=true: like the system-profile listing above, the
		// read-only audit must never block on a sudo password prompt (pg2-ssp8).
		// `sudo -n` fails fast; deadPathsSize then reports "unknown" and this
		// section still renders.
		fmt.Fprintf(out, "Reclaimable (dead paths): %s\n", deadPathsSize(ctx, s.runner, true))
	}

	return nil
}

// auditProfile emits the per-profile audit block:
//
//	  <label>:
//	    Profile: <profile>
//	    <generation lines, each indented 4 spaces>
//	    Closure size: <size>
//	<blank line>
//
// Errors from listGenerations are tolerated (zero generation lines emitted).
// Closure size becomes "unknown" if profileClosureSize fails.
func (s *Store) auditProfile(ctx context.Context, out io.Writer, category, profile string, sudo bool) {
	label := s.formatProfileLabel(profile, category)
	fmt.Fprintf(out, "  %s:\n", label)
	fmt.Fprintf(out, "    Profile: %s\n", profile)

	// nonInteractive=true: `audit` is read-only and must never block on a sudo
	// password prompt (pg2-ssp8). With `sudo -n`, an absent credential fails
	// fast; the tolerate-error path below then emits the header with no
	// generations and the remaining sudo-free sections still render.
	gens, err := listGenerations(ctx, s.runner, profile, sudo, true)
	if err == nil {
		for _, g := range gens {
			currentWord := ""
			if g.Current {
				currentWord = "current"
			}
			fmt.Fprintf(out, "    %d %s %s\n", g.Num, g.Date, currentWord)
		}
	}

	fmt.Fprintf(out, "    Closure size: %s\n", profileClosureSize(ctx, s.runner, profile))
	fmt.Fprintln(out)
}
