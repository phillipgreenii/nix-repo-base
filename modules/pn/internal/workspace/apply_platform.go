package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// activationPlatform is the platform-specific part of apply's build-as-user flow
// (Strategy pattern): which file proves the built system can be activated, how
// that is probed, root's home directory for the set-profile step and how the
// activation command is rewritten. It is selected from the program named in
// activate_command, which is explicit configuration, never from the host or from
// the {builder} placeholder.
type activationPlatform struct {
	name string
	// rootHome is root's home directory, set explicitly in the set-profile step.
	rootHome string
	// checkOut validates the platform's own file of an already store-validated out path.
	checkOut func(out string) error
	// probe returns "" when the built system can be activated, else the reason.
	probe func(ctx context.Context, ws *Workspace, runDir, out string) (exec.Result, string)
	// activateWhat names the activation in the probe failure text.
	activateWhat string
	// wrapActivate rewrites the substituted activate argv (sudo included) for the
	// platform; nil leaves it untouched (darwin). systemd reports whether the host
	// runs systemd, decided once in the preflight.
	wrapActivate func(argv []string, systemd bool) []string
	// probeAdvice, when set, is appended to the probe failure text; out is the
	// built system. nil (darwin) appends nothing.
	probeAdvice func(out string) string
}

const (
	darwinActivateProgram = "{out}/sw/bin/darwin-rebuild"
	nixosActivateProgram  = "{out}/bin/switch-to-configuration"
)

// platformForActivate selects the platform from the activate program, the
// element after sudo of the activate_command template (validateActivateCommand
// already guarantees it begins with {out}/). The NixOS template MUST be exactly
// `sudo {out}/bin/switch-to-configuration switch`: pn always sets the system
// profile first, so only the switch action matches the flow (boot, test and
// dry-activate would leave /run/current-system unchanged or skip what pn's
// post-conditions need).
func platformForActivate(activateTmpl string) (activationPlatform, error) {
	fields := strings.Fields(activateTmpl)
	if len(fields) >= 2 {
		switch fields[1] {
		case darwinActivateProgram:
			return darwinPlatformStrategy(), nil
		case nixosActivateProgram:
			if len(fields) != 3 || fields[2] != "switch" {
				return activationPlatform{}, fmt.Errorf("activate_command %q: on nixos the template must be exactly %q: pn always sets the system profile first, so only the switch action is accepted (not boot, test, dry-activate or check, and no extra arguments)",
					activateTmpl, "sudo "+nixosActivateProgram+" switch")
			}
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
		name:         "darwin",
		activateWhat: "darwin-rebuild activate",
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

// nixosUnit is the transient unit the NixOS activation runs in. It is the name
// nixos-rebuild uses, so a concurrent nixos-rebuild switch and pn exclude each other.
const nixosUnit = "nixos-rebuild-switch-to-configuration"

func nixosPlatformStrategy() activationPlatform {
	return activationPlatform{
		name:         "nixos",
		activateWhat: "switch-to-configuration switch",
		// Root's home on NixOS is /root (getent passwd root).
		rootHome: "/root",
		checkOut: func(out string) error {
			if err := requireExecutable(filepath.Join(out, "bin", "switch-to-configuration"), "bin/switch-to-configuration"); err != nil {
				return err
			}
			// nixos-rebuild refuses a closure without a regular nixos-version file
			// (a nix bug can leave a partial closure), so pn does too.
			vp := filepath.Join(out, "nixos-version")
			fi, err := os.Stat(vp)
			if err != nil {
				return fmt.Errorf("built system has no nixos-version: %w", err)
			}
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file (mode %s)", vp, fi.Mode())
			}
			f, err := os.Open(vp)
			if err != nil {
				return fmt.Errorf("nixos-version is not readable: %w", err)
			}
			return f.Close()
		},
		probe: func(ctx context.Context, ws *Workspace, runDir, out string) (exec.Result, string) {
			return ws.probeUsage(ctx, runDir, filepath.Join(out, "bin", "switch-to-configuration"), nil, parseSwitchToConfigurationUsage)
		},
		probeAdvice: func(out string) string {
			return ". This is a pn check, not a system fault. To see what the built system prints, run " +
				filepath.Join(out, "bin", "switch-to-configuration") + " with no arguments, as your user (it only prints usage)"
		},
		wrapActivate: wrapNixOSActivate,
	}
}

// wrapNixOSActivate rewrites [sudo, <out>/bin/switch-to-configuration, switch]
// to the argv `nixos-rebuild switch --sudo` uses: the environment travels as
// env -i arguments (sudo drops the caller's environment), and when systemd is
// present the activation runs in a transient unit, so a dropped terminal cannot
// kill a half-finished activation. NIXOS_INSTALL_BOOTLOADER=0 matches
// nixos-rebuild without --install-bootloader. pn deliberately does not forward
// LOCALE_ARCHIVE (the switch-to-configuration wrapper sets it) or NIXOS_NO_CHECK
// (a root step must not let an unprivileged variable skip the pre-switch checks).
func wrapNixOSActivate(argv []string, systemd bool) []string {
	out := []string{argv[0], "env", "-i", "PATH=" + rootPath, "NIXOS_INSTALL_BOOTLOADER=0"}
	if systemd {
		out = append(out, "systemd-run", "-E", "NIXOS_INSTALL_BOOTLOADER", "--collect",
			"--no-ask-password", "--pipe", "--quiet", "--service-type=exec", "--unit="+nixosUnit)
	}
	return append(out, argv[1:]...)
}

// parseSwitchToConfigurationUsage decides whether a probe run of
// `<out>/bin/switch-to-configuration` with no arguments shows a script that can
// `switch`. It prints its usage to STDERR and exits 1 (exit 1 is the ACCEPTED
// outcome). The usage is matched by keyword (switch and boot) rather than
// exactly, so a nixpkgs rewording does not block every apply. A nil error means usable.
func parseSwitchToConfigurationUsage(exitCode int, stdout, stderr []byte) error {
	first, _, _ := strings.Cut(string(stderr), "\n")
	if exitCode != 1 {
		return fmt.Errorf("a run with no arguments exited %d, want 1 (first stderr line %q)", exitCode, first)
	}
	if len(stdout) != 0 {
		return errors.New("a run with no arguments wrote to stdout, want usage on stderr only")
	}
	text := string(stderr)
	if !strings.Contains(text, "switch") || !strings.Contains(text, "boot") {
		return fmt.Errorf("usage output %q does not mention the switch and boot actions", first)
	}
	return nil
}

// systemdPresent reports whether path (/run/systemd/system in production)
// exists: the test nixos-rebuild uses to decide on systemd-run.
func systemdPresent(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
