package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// activationPlatform is the platform-specific part of apply's build-as-user flow
// (Strategy pattern): which file proves the built system can be activated, how
// that is probed, and root's home directory for the set-profile step. It is
// selected from the program named in activate_command, which is explicit
// configuration, never from the host or from the {builder} placeholder.
type activationPlatform struct {
	name string
	// rootHome is root's home directory, set explicitly in the set-profile step.
	rootHome string
	// checkOut validates the platform's own file of an already store-validated out path.
	checkOut func(out string) error
	// probe returns "" when the built system can be activated, else the reason.
	probe func(ctx context.Context, ws *Workspace, runDir, out string) (exec.Result, string)
}

const (
	darwinActivateProgram = "{out}/sw/bin/darwin-rebuild"
	nixosActivateProgram  = "{out}/bin/switch-to-configuration"
)

// platformForActivate selects the platform from the activate program, the
// element after sudo of the activate_command template (validateActivateCommand
// already guarantees it begins with {out}/).
func platformForActivate(activateTmpl string) (activationPlatform, error) {
	// TODO(nixos): the NixOS activation is assumed to be plain
	// `sudo <out>/bin/switch-to-configuration switch`. `nixos-rebuild switch` runs it under
	// systemd-run (newer versions) and sets LOCALE_ARCHIVE; compare on a NixOS host (read the
	// nixos-rebuild script) and, if the difference matters, change the documented
	// activate_command (the program must stay {out}/bin/switch-to-configuration) or the
	// root step options. See NIXOS_TODO.md.
	fields := strings.Fields(activateTmpl)
	if len(fields) >= 2 {
		switch fields[1] {
		case darwinActivateProgram:
			return darwinPlatformStrategy(), nil
		case nixosActivateProgram:
			return nixosPlatformStrategy(), nil
		}
	}
	return activationPlatform{}, fmt.Errorf("activate_command %q: the program must be %s (darwin) or %s (nixos)",
		activateTmpl, darwinActivateProgram, nixosActivateProgram)
}

// requireExecutable checks that path (following symlinks) is an executable regular file.
func requireExecutable(path, what string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("built system has no %s: %w", what, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s does not resolve to an executable regular file (mode %s)", path, fi.Mode())
	}
	return nil
}

func darwinPlatformStrategy() activationPlatform {
	return activationPlatform{
		name: "darwin",
		// macOS sudo preserves the caller's HOME, so root's home is set explicitly.
		rootHome: "/var/root",
		checkOut: func(out string) error {
			// os.Stat follows symlinks: the invoked path must resolve to an executable file.
			return requireExecutable(filepath.Join(out, "sw", "bin", "darwin-rebuild"), "sw/bin/darwin-rebuild")
		},
		probe: func(ctx context.Context, ws *Workspace, runDir, out string) (exec.Result, string) {
			return ws.probeActivate(ctx, runDir, out)
		},
	}
}

func nixosPlatformStrategy() activationPlatform {
	return activationPlatform{
		name: "nixos",
		// TODO(nixos): root's home on NixOS is assumed to be /root. Verify on a NixOS
		// host: `getent passwd root | cut -d: -f6`. If it differs, change rootHome here
		// and the HOME= expectation in TestSetProfileArgv_NixOSUsesRootHome.
		rootHome: "/root",
		checkOut: func(out string) error {
			// TODO(nixos): a NixOS system closure is assumed to hold an executable
			// bin/switch-to-configuration (and activate, checked for every platform).
			// Verify with TestDriftGuard_NixOSSystemLayout and the spike in NIXOS_TODO.md.
			return requireExecutable(filepath.Join(out, "bin", "switch-to-configuration"), "bin/switch-to-configuration")
		},
		probe: func(context.Context, *Workspace, string, string) (exec.Result, string) {
			// TODO(nixos): no process probe: the file check in checkOut is the whole
			// pre-root check. Verify on a NixOS host that `<out>/bin/switch-to-configuration`
			// run with no arguments prints usage (so a real probe could be added); if the
			// file check proves too weak, add a probe here. See NIXOS_TODO.md.
			return exec.Result{}, ""
		},
	}
}
