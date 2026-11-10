package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the activationPlatform Strategy (ADR 0038 as amended): apply works
// on darwin and NixOS, chosen from the program named in activate_command.

const (
	darwinActivateTmpl = "sudo {out}/sw/bin/darwin-rebuild activate"
	nixosActivateTmpl  = "sudo {out}/bin/switch-to-configuration switch"
)

// darwinPlatform / nixosPlatform resolve the two strategies the way production
// does, from their activate_command templates.
func darwinPlatform(t *testing.T) activationPlatform {
	t.Helper()
	p, err := platformForActivate(darwinActivateTmpl)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func nixosPlatform(t *testing.T) activationPlatform {
	t.Helper()
	p, err := platformForActivate(nixosActivateTmpl)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newFakeNixOSSystem lays out <store>/<hash>-nixos-system-test with an
// executable activate and bin/switch-to-configuration and no sw/bin/darwin-rebuild.
// TODO(nixos): the layout (a regular executable activate and bin/switch-to-configuration
// directly in the system closure) is the documented NixOS shape; verify it on a
// NixOS host with TestDriftGuard_NixOSSystemLayout and the spike in NIXOS_TODO.md.
func newFakeNixOSSystem(t *testing.T, store, hash string) fakeSystem {
	t.Helper()
	out := filepath.Join(store, hash+"-nixos-system-test")
	if err := os.MkdirAll(filepath.Join(out, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{out + "/activate", out + "/bin/switch-to-configuration"} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fakeSystem{store: store, out: out}
}

func TestPlatformForActivate(t *testing.T) {
	d, err := platformForActivate(darwinActivateTmpl)
	if err != nil || d.name != "darwin" || d.rootHome != "/var/root" {
		t.Errorf("darwin = %+v, %v", d, err)
	}
	n, err := platformForActivate(nixosActivateTmpl)
	if err != nil || n.name != "nixos" || n.rootHome != "/root" {
		t.Errorf("nixos = %+v, %v", n, err)
	}
	_, err = platformForActivate("sudo {out}/bin/other activate")
	if err == nil || !strings.Contains(err.Error(), "{out}/sw/bin/darwin-rebuild") || !strings.Contains(err.Error(), "{out}/bin/switch-to-configuration") {
		t.Errorf("unrecognised program: err = %v, want one naming both accepted programs", err)
	}
}

func TestSetProfileArgv_DarwinIsUnchanged(t *testing.T) {
	got := strings.Join(setProfileArgv("/nix/store/x-out", darwinPlatform(t)), " ")
	want := "sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set /nix/store/x-out"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSetProfileArgv_NixOSUsesRootHome(t *testing.T) {
	got := strings.Join(setProfileArgv("/nix/store/x-out", nixosPlatform(t)), " ")
	want := "sudo env -i HOME=/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set /nix/store/x-out"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestValidateBuildOut_PerPlatform(t *testing.T) {
	store := t.TempDir()
	darwin := newFakeSystem(t, store, fakeHash)
	nixos := newFakeNixOSSystem(t, store, fakeHash2)
	cases := []struct {
		name     string
		out      string
		platform activationPlatform
		want     string // "" = accepted, else an error substring
	}{
		{"darwin system, darwin platform", darwin.out, darwinPlatform(t), ""},
		{"nixos system, nixos platform", nixos.out, nixosPlatform(t), ""},
		{"nixos system, darwin platform", nixos.out, darwinPlatform(t), "sw/bin/darwin-rebuild"},
		{"darwin system, nixos platform", darwin.out, nixosPlatform(t), "bin/switch-to-configuration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBuildOut(tc.out, store, tc.platform)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("rejected: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// switch-to-configuration must be an executable regular file.
	nonExec := newFakeNixOSSystem(t, store, "0123456789abcdfghijklmnpqrsvwxyy")
	if err := os.Chmod(nonExec.out+"/bin/switch-to-configuration", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildOut(nonExec.out, store, nixosPlatform(t)); err == nil || !strings.Contains(err.Error(), "executable") {
		t.Errorf("a non-executable switch-to-configuration was accepted: %v", err)
	}
}

// The NixOS twin of the darwin drift guard: on a NixOS host the running system
// has the layout the NixOS platform validates. Skips elsewhere.
func TestDriftGuard_NixOSSystemLayout(t *testing.T) {
	// TODO(nixos): this guard only runs on a NixOS host; it is the one automatic
	// check that bin/switch-to-configuration and activate are regular executables
	// in a system closure. Run it natively on NixOS (see NIXOS_TODO.md).
	for _, p := range []string{"/run/current-system/activate", "/run/current-system/bin/switch-to-configuration"} {
		if _, err := os.Stat(p); err != nil {
			t.Skip("not a NixOS host")
		}
	}
	for _, p := range []string{"/run/current-system/activate", "/run/current-system/bin/switch-to-configuration"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not an executable regular file (mode %s)", p, fi.Mode())
		}
	}
}
