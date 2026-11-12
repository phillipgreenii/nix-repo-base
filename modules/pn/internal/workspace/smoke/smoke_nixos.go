//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// assertS46NixOSApply: apply ran the NixOS build-as-user flow end to end against
// stand-ins (ADR 0043). The two root steps are the documented argv: set-profile
// with root's home /root, then the activation wrapped the way nixos-rebuild wraps
// it, with or without systemd-run according to PN_SMOKE_SYSTEMD. The out path
// travels as argv only.
func assertS46NixOSApply(t *testing.T, wsRoot string, env []string) {
	t.Helper()
	outLines, ok := readArgvFile(t, filepath.Join(wsRoot, "expected.out"))
	if !ok || len(outLines) != 1 {
		t.Fatalf("S46: expected.out = %q (ok=%v)", outLines, ok)
	}
	out := outLines[0]
	state := envValue(env, "XDG_STATE_HOME")
	systemd := envValue(env, "PN_SMOKE_SYSTEMD")
	if systemd != "0" && systemd != "1" {
		t.Fatalf("S46: PN_SMOKE_SYSTEMD = %q, the scenario needs 0 or 1", systemd)
	}

	if build, ok := readArgvFile(t, filepath.Join(wsRoot, "build.argv")); !ok || len(build) == 0 || build[0] != "build" {
		t.Errorf("S46: nixos-rebuild argv = %q (ran=%v), want it to start with build", build, ok)
	}

	wantSetProfile := []string{
		"env", "-i", "HOME=/root", "NIX_REMOTE=daemon",
		"PATH=/run/current-system/sw/bin:/usr/bin:/bin",
		"/run/current-system/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", out,
	}
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.1.argv")); !ok || !reflect.DeepEqual(got, wantSetProfile) {
		t.Errorf("S46: first sudo argv = %q (ran=%v), want %q", got, ok, wantSetProfile)
	}
	wantActivate := []string{"env", "-i", "PATH=/run/current-system/sw/bin:/usr/bin:/bin", "NIXOS_INSTALL_BOOTLOADER=0"}
	if systemd == "1" {
		wantActivate = append(wantActivate, "systemd-run", "-E", "NIXOS_INSTALL_BOOTLOADER", "--collect",
			"--no-ask-password", "--pipe", "--quiet", "--service-type=exec", "--unit=nixos-rebuild-switch-to-configuration")
	}
	wantActivate = append(wantActivate, out+"/bin/switch-to-configuration", "switch")
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.2.argv")); !ok || !reflect.DeepEqual(got, wantActivate) {
		t.Errorf("S46: second sudo argv = %q (ran=%v), want %q", got, ok, wantActivate)
	}
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.3.argv")); ok {
		t.Error("S46: a third sudo ran")
	}
	for _, n := range []string{"1", "2"} {
		data, err := os.ReadFile(filepath.Join(wsRoot, "sudo."+n+".env"))
		if err != nil {
			t.Fatalf("S46: %v", err)
		}
		if strings.Contains(string(data), out) {
			t.Errorf("S46: the out path leaked into sudo %s's environment", n)
		}
	}
	log, err := os.ReadFile(filepath.Join(state, "pn", "apply-logs", "latest"))
	if err != nil {
		t.Fatalf("S46: no apply log: %v", err)
	}
	if !strings.Contains(string(log), "failed_step: none\n") || !strings.Contains(string(log), "result: success\n") {
		t.Errorf("S46: apply log trailer:\n%s", log)
	}
}

// assertS47NixOSNoRootStep: the probe failed, so neither sudo call happened and
// the built system's activate never ran; the user build did run.
func assertS47NixOSNoRootStep(t *testing.T, wsRoot string) {
	t.Helper()
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.1.argv")); ok {
		t.Error("S47: a root step ran although the probe failed")
	}
	if _, err := os.Stat(filepath.Join(wsRoot, "activate-ran.txt")); err == nil {
		t.Error("S47: the system's activate ran although the probe failed")
	}
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "build.argv")); !ok {
		t.Error("S47: the user build never ran")
	}
}
