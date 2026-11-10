//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// assertS42BuildAsUserApply: apply ran the build-as-user flow end to end against
// stand-ins (pg2-sdbhh4.9, ADR 0038). The user build ran in a private run dir
// that is gone again, the two root steps are exactly the documented argv with the
// validated out path as an argument (never in the environment).
func assertS42BuildAsUserApply(t *testing.T, wsRoot string, env []string) {
	t.Helper()
	outLines, ok := readArgvFile(t, filepath.Join(wsRoot, "expected.out"))
	if !ok || len(outLines) != 1 {
		t.Fatalf("S42: expected.out = %q (ok=%v)", outLines, ok)
	}
	out := outLines[0]
	state := envValue(env, "XDG_STATE_HOME")

	// The build: cwd is a private run dir under pn's state dir, which is not the
	// terminal repo and is removed again.
	cwdLines, ok := readArgvFile(t, filepath.Join(wsRoot, "build.cwd"))
	if !ok || len(cwdLines) != 1 {
		t.Fatalf("S42: the user build never ran (build.cwd = %q, ok=%v)", cwdLines, ok)
	}
	physState, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	runDir := cwdLines[0]
	wantParent := filepath.Join(physState, "pn", "apply-build")
	if filepath.Dir(runDir) != wantParent || !strings.HasPrefix(filepath.Base(runDir), "run-") {
		t.Errorf("S42: build ran in %q, want a run-* directory under %s", runDir, wantParent)
	}
	if _, err := os.Stat(runDir); err == nil {
		t.Errorf("S42: the run dir %s was not removed after the apply", runDir)
	}
	if build, ok := readArgvFile(t, filepath.Join(wsRoot, "build.argv")); !ok || !reflect.DeepEqual(build, []string{"build"}) {
		t.Errorf("S42: darwin-rebuild argv = %q (ran=%v), want [build]", build, ok)
	}

	// The two root steps, exact argv.
	wantSetProfile := []string{
		"env", "-i", "HOME=/var/root", "NIX_REMOTE=daemon",
		"PATH=/run/current-system/sw/bin:/usr/bin:/bin",
		"/run/current-system/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", out,
	}
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.1.argv")); !ok || !reflect.DeepEqual(got, wantSetProfile) {
		t.Errorf("S42: first sudo argv = %q (ran=%v), want %q", got, ok, wantSetProfile)
	}
	wantActivate := []string{out + "/sw/bin/darwin-rebuild", "activate"}
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.2.argv")); !ok || !reflect.DeepEqual(got, wantActivate) {
		t.Errorf("S42: second sudo argv = %q (ran=%v), want %q", got, ok, wantActivate)
	}
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.3.argv")); ok {
		t.Error("S42: a third sudo ran")
	}
	// The out path travels as argv only.
	for _, n := range []string{"1", "2"} {
		data, err := os.ReadFile(filepath.Join(wsRoot, "sudo."+n+".env"))
		if err != nil {
			t.Fatalf("S42: %v", err)
		}
		if strings.Contains(string(data), out) {
			t.Errorf("S42: the out path leaked into sudo %s's environment", n)
		}
	}
	// The apply log names no failed step.
	log, err := os.ReadFile(filepath.Join(state, "pn", "apply-logs", "latest"))
	if err != nil {
		t.Fatalf("S42: no apply log: %v", err)
	}
	if !strings.Contains(string(log), "failed_step: none\n") || !strings.Contains(string(log), "result: success\n") {
		t.Errorf("S42: apply log trailer:\n%s", log)
	}
}

// assertS44NoRootStep: the probe failed, so neither sudo call happened and the
// built system's activate never ran.
func assertS44NoRootStep(t *testing.T, wsRoot string) {
	t.Helper()
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.1.argv")); ok {
		t.Error("S44: a root step ran although the probe failed")
	}
	if _, err := os.Stat(filepath.Join(wsRoot, "activate-ran.txt")); err == nil {
		t.Error("S44: the system's activate ran although the probe failed")
	}
	// The user build did run: the probe is after the build.
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "build.argv")); !ok {
		t.Error("S44: the user build never ran")
	}
}

// writeAsUserStandIns creates, under wsRoot, the stand-ins the build-as-user
// apply needs when a test (not a scenario setup.sh) drives pn: a fake system in
// the fake store (PN_SMOKE_STORE_DIR is wsRoot's physical path + /store, see
// buildScrubbedEnv), a darwin-rebuild that leaves ./result -> that system, and a
// sudo that records its argv (sudo.N.argv) and runs nothing. It returns the
// directory to put first on PATH. Same shape as S42's setup.sh.
func writeAsUserStandIns(t *testing.T, wsRoot string) string {
	t.Helper()
	phys, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatal(err)
	}
	// 32 characters of the nix base32 alphabet.
	out := filepath.Join(phys, "store", "0123456789abcdfghijklmnpqrsvwxyz-darwin-system-smoke")
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(out, "activate"), "#!/bin/sh\nexit 0\n")
	write(filepath.Join(out, "sw", "bin", "darwin-rebuild"),
		"#!/bin/sh\necho \"darwin-rebuild [--help] {edit | switch | activate | build | check | changelog}\" >&2\nexit 1\n")
	fakebin := filepath.Join(phys, "fakebin")
	write(filepath.Join(fakebin, "darwin-rebuild"), "#!/bin/sh\nln -sfn \""+out+"\" result\n")
	write(filepath.Join(fakebin, "sudo"),
		"#!/bin/sh\nn=1\nwhile [ -e \""+phys+"/sudo.$n.argv\" ]; do n=$((n + 1)); done\nprintf '%s\\n' \"$@\" >\""+phys+"/sudo.$n.argv\"\nexit 0\n")
	return fakebin
}
