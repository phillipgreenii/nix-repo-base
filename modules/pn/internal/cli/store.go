package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/store"
)

// isInteractive reports whether stdin is a terminal. It is a package var so
// tests can override it. Uses a char-device check (stdlib only — no extra
// dependency to regenerate gomod2nix for).
var isInteractive = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// confirmDeepClean decides whether the destructive `store deepclean` may
// proceed (bead pg2-w0y8u). A dry run or an explicit --yes proceeds without
// prompting. Otherwise an interactive session is prompted (default No) and a
// non-interactive session is refused — deepclean makes privileged, destructive
// changes (sudo nix-store --gc plus profile-generation deletions) that must not
// happen on a bare `pn store deepclean` typed by accident or from a script.
func confirmDeepClean(in io.Reader, errOut io.Writer, dryRun, yes, interactive bool) (proceed bool, err error) {
	if dryRun || yes {
		return true, nil
	}
	if !interactive {
		return false, fmt.Errorf(
			"deepclean makes privileged, destructive changes (sudo nix-store --gc and profile-generation deletions); " +
				"refusing to run non-interactively — re-run with --yes to confirm",
		)
	}
	fmt.Fprintln(errOut, "pn store deepclean will delete old profile generations and run 'sudo nix-store --gc' (destructive and privileged).")
	fmt.Fprint(errOut, "Proceed? [y/N]: ")
	if readYes(in) {
		return true, nil
	}
	fmt.Fprintln(errOut, "Aborted.")
	return false, nil
}

// readYes reads one line from r and reports whether it is affirmative
// (y / yes, case-insensitive). EOF or anything else is a No.
func readYes(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func addStoreCmd(parent *cobra.Command) {
	s := &cobra.Command{
		Use:   "store",
		Short: "Operate on the nix store",
		Long: `Audit and reclaim space in the local Nix store.

Subcommands:
  audit      Read-only report of profile generations, closure sizes, and store usage.
  deepclean  Prune old generations and stale GC roots, garbage-collect, then optimise the store.

Configuration lives in ~/.config/pn/store.toml (search_dirs, keep_days, keep_count).
See docs/pn-store.md for user journeys and retention semantics.`,
	}
	s.AddCommand(storeAuditCmd())
	s.AddCommand(storeDeepCleanCmd())
	parent.AddCommand(s)
}

func storeAuditCmd() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit nix store contents",
		Long: `Audit Nix profile generations and store size.

Reports, in order: System Profiles, Home Manager, User Profiles, Devbox Global,
Devbox Projects, and Nix Store (volume used). Read-only — makes no changes.

With --full, also estimates reclaimable space from dead store paths
(runs 'sudo nix-store --gc --print-dead'; slow).

Examples:
  # Show profile generations and store usage
  pn store audit

  # Include the reclaimable-space estimate
  pn store audit --full`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(exec.NewRealRunner()).Audit(
				cmd.Context(),
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
				store.AuditOptions{Full: full},
			)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "Include dead paths estimate (slow, requires sudo)")
	return cmd
}

func storeDeepCleanCmd() *cobra.Command {
	var dryRun bool
	var yes bool
	var keepSince string
	var keep int
	cmd := &cobra.Command{
		Use:   "deepclean",
		Short: "Aggressive nix store cleanup",
		Long: `pn-store-deepclean: Clean old Nix profile generations, stale GC roots, and garbage collect the store

Cleans:
  - System, home-manager, user, devbox profile generations
  - Result symlinks (nix build outputs) in search dirs
  - Stale ~/.nix-profiles/ entries (mtime older than --keep-since)
  - NH temp roots in TMPDIR

After pruning it runs 'sudo nix-store --gc' then 'nix store optimise' (hard-links
duplicate files; this is the batched replacement for auto-optimise-store, which
is disabled so flake-update fetches stay fast).

Finally, shows runtime roots summary (store paths held by running processes that
could be freed by restarting applications).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			proceed, err := confirmDeepClean(cmd.InOrStdin(), cmd.ErrOrStderr(), dryRun, yes, isInteractive())
			if err != nil {
				return err
			}
			if !proceed {
				return nil
			}
			return store.New(exec.NewRealRunner()).DeepClean(
				cmd.Context(),
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
				store.DeepCleanOptions{DryRun: dryRun, KeepSince: keepSince, Keep: keep},
			)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be cleaned without deleting")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (required for non-interactive/scripted use)")
	cmd.Flags().StringVar(&keepSince, "keep-since", "", "Keep generations newer than this (e.g. 14d, 2w)")
	cmd.Flags().IntVar(&keep, "keep", -1, "Keep N most recent generations (-1 = config default)")
	return cmd
}
