// Package cli wires the pn binary's cobra command tree.
package cli

import (
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// addOSXCmdHook is set by osx_darwin.go's init() on darwin. Nil on other platforms.
var addOSXCmdHook func(*cobra.Command)

// Execute builds the root command tree and runs it against os.Args[1:].
// version must be a real version string from mkVersion; "dev" is rejected.
func Execute(version string) error {
	return executeWithVersion(version, os.Args[1:], os.Stdout, os.Stderr)
}

// executeWithVersion is the test seam — caller passes args, stdout, stderr writers.
func executeWithVersion(version string, args []string, stdout, stderr io.Writer) error {
	if version == "dev" {
		return errors.New("pn: built with version=\"dev\"; this binary was built outside the Nix derivation. Use `nix build .#pn` for a real binary")
	}

	root := newRootCmd(version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
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
	addWorkspaceCmd(root)
	addStoreCmd(root)
	if addOSXCmdHook != nil {
		addOSXCmdHook(root)
	}
	return root
}
