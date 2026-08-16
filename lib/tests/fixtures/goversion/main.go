// Command goversion is a dependency-free Pattern-A fixture for the Go
// builders' baseVersion / ldflags / env passthrough check
// (checks.go-builders-binary-passthrough, bead tc-5lxy.26). It prints the
// linker-injected version and nothing else, so the check can compare the
// binary's RUNTIME version against the derivation's `version` attribute and
// prove the `-X main.Version=` injection carried the caller's `baseVersion`.
// Used only by base's own flake checks; never shipped.
package main

import (
	"fmt"
	"os"
)

// Version is the symbol mkGoApp targets by default (`-X main.Version=...`,
// mkGoBinary's default versionPath). The placeholder must never survive a real
// build — the check fails if the binary still prints it.
var Version = "unset"

func main() {
	// fmt.Fprintln is in .golangci.yml's errcheck exclusions (matching base's
	// own convention), so leaving its write unchecked is intentionally allowed.
	fmt.Fprintln(os.Stdout, Version)
}
