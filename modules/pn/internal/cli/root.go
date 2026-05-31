// Package cli wires the pn binary's cobra command tree.
package cli

import (
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Execute builds the root command tree and runs it against os.Args[1:].
// version must be a real version string from mkVersion; "dev" is rejected.
func Execute(version string) error {
	return executeWithVersion(version, os.Args[1:], os.Stderr)
}

// executeWithVersion is the test seam — caller passes args and stderr writer.
func executeWithVersion(version string, args []string, stderr io.Writer) error {
	if version == "dev" {
		return errors.New("pn: built with version=\"dev\"; this binary was built outside the Nix derivation. Use `nix build .#pn` for a real binary")
	}

	root := newRootCmd(version)
	root.SetArgs(args)
	root.SetErr(stderr)
	root.SetOut(stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true
	return root.Execute()
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "pn",
		Short:   "pn-workspace multi-repo Nix workflow tool",
		Version: version,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	return root
}
