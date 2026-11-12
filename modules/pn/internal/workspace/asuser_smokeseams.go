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
		// The NixOS platform's systemd decision comes from PN_SMOKE_SYSTEMD (0 or 1),
		// never from the host: with the variable unset or invalid the seam fails
		// loudly instead of reading /run/systemd/system. Darwin scenarios never
		// consult it.
		switch v := os.Getenv("PN_SMOKE_SYSTEMD"); v {
		case "0", "1":
			present := v == "1"
			defaultAsUserSeams.systemdPresent = func() bool { return present }
		default:
			defaultAsUserSeams.systemdPresent = func() bool {
				panic("smoke pn: PN_SMOKE_SYSTEMD must be 0 or 1 for a NixOS scenario (got \"" + v + "\"); the smoke run never reads the host's /run/systemd/system")
			}
		}
	}
}
