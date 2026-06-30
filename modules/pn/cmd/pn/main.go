// Command pn is the entrypoint for the pn-workspace multi-repo workflow tool.
//
// Version is set at build time via -X main.Version=<...> by mkGoBuilders.
// The default "dev" is intentional: it's a guard that fails fast if the
// binary was built outside the Nix derivation.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/cli"
)

var Version = "dev"

func main() {
	err := cli.Execute(Version)
	if code := cli.ExitCode(err); code != 0 {
		// Print the message only for plain errors; ExitCodeError-carrying
		// commands have already rendered their own report to stdout.
		var ec cli.ExitCodeError
		if !errors.As(err, &ec) {
			fmt.Fprintln(os.Stderr, err)
		} else if ec.Msg != "" {
			fmt.Fprintln(os.Stderr, ec.Msg)
		}
		os.Exit(code)
	}
}
