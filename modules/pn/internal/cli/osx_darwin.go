//go:build darwin

package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/osx"
)

func init() {
	addOSXCmdHook = addOSXCmd
}

func addOSXCmd(parent *cobra.Command) {
	o := &cobra.Command{
		Use:   "osx",
		Short: "macOS-specific commands",
	}
	o.AddCommand(osxTCCCheckCmd())
	parent.AddCommand(o)
}

func osxTCCCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tcc-check",
		Short: "Check macOS Terminal TCC permissions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return osx.New(exec.NewRealRunner()).Check(context.Background(), osx.CheckOptions{})
		},
	}
}
