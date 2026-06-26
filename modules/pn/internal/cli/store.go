package cli

import (
	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/store"
)

func addStoreCmd(parent *cobra.Command) {
	s := &cobra.Command{
		Use:   "store",
		Short: "Operate on the nix store",
		Long: `Audit and reclaim space in the local Nix store.

Subcommands:
  audit      Read-only report of profile generations, closure sizes, and store usage.
  deepclean  Prune old generations and stale GC roots, then garbage-collect the store.

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

After cleanup, shows runtime roots summary (store paths held by running
processes that could be freed by restarting applications).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(exec.NewRealRunner()).DeepClean(
				cmd.Context(),
				cmd.OutOrStdout(),
				cmd.ErrOrStderr(),
				store.DeepCleanOptions{DryRun: dryRun, KeepSince: keepSince, Keep: keep},
			)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be cleaned without deleting")
	cmd.Flags().StringVar(&keepSince, "keep-since", "", "Keep generations newer than this (e.g. 14d, 2w)")
	cmd.Flags().IntVar(&keep, "keep", -1, "Keep N most recent generations (-1 = config default)")
	return cmd
}
