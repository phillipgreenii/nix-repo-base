package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/store"
)

func addStoreCmd(parent *cobra.Command) {
	s := &cobra.Command{
		Use:   "store",
		Short: "Operate on the nix store",
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
	return &cobra.Command{
		Use:   "deepclean",
		Short: "Aggressive nix store cleanup",
		RunE: func(cmd *cobra.Command, args []string) error {
			return store.New(exec.NewRealRunner()).DeepClean(context.Background(), store.DeepCleanOptions{})
		},
	}
}
