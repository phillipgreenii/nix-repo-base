// Command pg-nix-log-wrapped runs a nix command unchanged while turning nix's
// --json-log-path activity stream into OpenTelemetry spans and metrics.
//
// Version is set at build time via -X main.Version=<...> by mkGoBuilders.
package main

import (
	"os"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/app"
)

var Version = "dev"

func main() {
	os.Exit(app.Run(app.DefaultDeps(Version)))
}
