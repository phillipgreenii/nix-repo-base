//go:build smokeseams

package workspace

import (
	"os"
	"time"
)

// Smoke-harness seams (bead pg2-sdbhh4.9). The smoke harness builds its pn binary
// with `-tags smokeseams` so scenario S42 can run apply's build-as-user flow
// against a fake store directory (a real /nix/store cannot be written to by a
// test) and without a real /run/current-system/sw/bin/nix-env. No shipped binary
// carries this file, and there is deliberately no environment variable that moves
// the store directory in a normal build: the out path is validated before root
// executes it.
func init() {
	// The smoke binary refreshes the sudo session every 100 ms so scenario S45
	// sees several refreshes during a one-second stand-in build. Production
	// binaries keep the 60 s default: this file only builds under the tag.
	sudoRefreshInterval = 100 * time.Millisecond

	if dir := os.Getenv("PN_SMOKE_STORE_DIR"); dir != "" {
		defaultAsUserSeams = &asUserSeams{nixEnvUsable: func() bool { return true }, storeDir: dir}
	}
}
