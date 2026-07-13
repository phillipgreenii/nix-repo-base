// Package cli wires the pn binary's cobra command tree.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

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

	// Cancel the command's context on SIGINT/SIGTERM so a Ctrl-C during a
	// fan-out (e.g. `pn workspace update`) propagates cancellation to the
	// in-flight subprocesses via cmd.Context(), instead of being ignored until
	// each child exits on its own (bead pg2-x3r0a). A second signal restores the
	// default behavior (immediate termination).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true
	return root.ExecuteContext(ctx)
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
