// Command pn is the entrypoint for the pn-workspace multi-repo workflow tool.
//
// Version is set at build time via -X main.Version=<...> by mkGoBuilders.
// The default "dev" is intentional: it's a guard that fails fast if the
// binary was built outside the Nix derivation.
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/cli"
)

var Version = "dev"

func main() {
	if err := cli.Execute(Version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
