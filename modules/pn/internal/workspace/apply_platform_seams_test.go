package workspace

import (
	"fmt"
	"os"
	"sync/atomic"
)

// productionSystemdReads counts how often a unit test reached the production
// systemd check, which would read the host's /run/systemd/system. No test may:
// every workspace the tests build installs the systemd seam explicitly
// (installAsUserSeams), so a unit run behaves the same on macOS and on Linux with
// or without systemd. The one test of the real default restores the function.
var productionSystemdReads atomic.Int32

func init() {
	productionSystemdPresent = func() bool {
		productionSystemdReads.Add(1)
		panic(fmt.Sprintf("a unit test reached the production systemd check (host %s): install the systemd seam with installAsUserSeams", systemdRunDir))
	}
	_ = os.Stdout
}

// nixosUsage is the verbatim stderr of a freshly built NixOS 26.05 system's
// bin/switch-to-configuration run with no arguments as an unprivileged user
// (exit 1, empty stdout), captured with the system closure of this workspace's
// homelab host. The fake runner answers the NixOS probe with it.
var nixosUsage = mustReadFixture("testdata/switch-to-configuration-usage.txt")

func mustReadFixture(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// nixosActivateWant is the literal activate argv of a NixOS apply for the out
// path o (ADR 0043), as one string, with or without systemd-run.
func nixosActivateWant(systemd bool, o string) string {
	const head = "sudo env -i PATH=/run/current-system/sw/bin:/usr/bin:/bin NIXOS_INSTALL_BOOTLOADER=0 "
	const run = "systemd-run -E NIXOS_INSTALL_BOOTLOADER --collect --no-ask-password --pipe --quiet --service-type=exec --unit=nixos-rebuild-switch-to-configuration "
	if systemd {
		return head + run + o + "/bin/switch-to-configuration switch"
	}
	return head + o + "/bin/switch-to-configuration switch"
}
