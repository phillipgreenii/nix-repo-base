// Command jira is a generic, tenant-agnostic Atlassian Jira access tool.
// It hard-codes no tenant, credential location, or OS-specific behavior;
// all of those are supplied as configuration (see modules/jira/README.md).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the jira CLI root. Subcommands are attached here.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "jira",
		Short:         "Generic Atlassian Jira access tool (issue / search / auth-status)",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Run is a no-op so cobra treats this as Runnable, which makes the
		// --help template include the UsageString (Use, Flags, etc.).
		Run: func(*cobra.Command, []string) {},
	}
	root.PersistentFlags().String("config", "", "path to config TOML (default: $XDG_CONFIG_HOME/jira/config.toml)")
	return root
}

func main() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
