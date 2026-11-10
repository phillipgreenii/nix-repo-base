//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	// Stateful like real sudo: -n -v fails until a -v ran, -k clears the ticket;
	// the three session forms go to sudo-session.log, everything else is numbered.
	log := phys + "/sudo-session.log"
	ticket := phys + "/sudo-ticket"
	write(filepath.Join(fakebin, "sudo"),
		"#!/bin/sh\ncase \"$*\" in\n"+
			"\"-n -v\") echo \"-n -v\" >>\""+log+"\"; [ -e \""+ticket+"\" ]; exit $? ;;\n"+
			"\"-v\") echo \"-v\" >>\""+log+"\"; : >\""+ticket+"\"; exit 0 ;;\n"+
			"\"-k\") echo \"-k\" >>\""+log+"\"; rm -f \""+ticket+"\"; exit 0 ;;\nesac\n"+
			"n=1\nwhile [ -e \""+phys+"/sudo.$n.argv\" ]; do n=$((n + 1)); done\nprintf '%s\\n' \"$@\" >\""+phys+"/sudo.$n.argv\"\nexit 0\n")
	return fakebin
}

// assertS45SudoSession: apply authenticated sudo first, kept the session warm
// while the (one second) stand-in build ran, and released it at the end (ADR
// 0042). The session calls are in sudo-session.log; sudo.1 and sudo.2 are still
// the two root steps.
func assertS45SudoSession(t *testing.T, wsRoot string) {
	t.Helper()
	lines, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo-session.log"))
	if !ok {
		t.Fatal("S45: no sudo-session.log: the session never ran")
	}
	if len(lines) < 6 {
		t.Fatalf("S45: sudo-session.log = %q, want probe, prompt, verify, at least two refreshes and a release", lines)
	}
	if !reflect.DeepEqual(lines[:3], []string{"-n -v", "-v", "-n -v"}) {
		t.Errorf("S45: session log starts %q, want [-n -v -v -n -v]", lines[:3])
	}
	if got := lines[len(lines)-1]; got != "-k" {
		t.Errorf("S45: session log ends with %q, want -k", got)
	}
	var prompts, probes, releases int
	for _, l := range lines {
		switch l {
		case "-v":
			prompts++
		case "-n -v":
			probes++
		case "-k":
			releases++
		default:
			t.Errorf("S45: unexpected session call %q", l)
		}
	}
	if prompts != 1 || releases != 1 {
		t.Errorf("S45: %d prompts and %d releases in %q, want exactly one of each", prompts, releases, lines)
	}
	if probes < 4 { // probe + verify + at least two refreshes
		t.Errorf("S45: %d -n -v calls in %q, want at least 4 (probe, verify, two refreshes)", probes, lines)
	}
	if _, err := os.Stat(filepath.Join(wsRoot, "sudo-ticket")); err == nil {
		t.Error("S45: the stand-in ticket survived the apply: -k did not run last")
	}

	out, _ := readArgvFile(t, filepath.Join(wsRoot, "expected.out"))
	if len(out) != 1 {
		t.Fatalf("S45: expected.out = %q", out)
	}
	wantSetProfile := []string{
		"env", "-i", "HOME=/var/root", "NIX_REMOTE=daemon",
		"PATH=/run/current-system/sw/bin:/usr/bin:/bin",
		"/run/current-system/sw/bin/nix-env", "-p", "/nix/var/nix/profiles/system", "--set", out[0],
	}
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.1.argv")); !ok || !reflect.DeepEqual(got, wantSetProfile) {
		t.Errorf("S45: sudo.1 argv = %q (ran=%v), want %q", got, ok, wantSetProfile)
	}
	wantActivate := []string{out[0] + "/sw/bin/darwin-rebuild", "activate"}
	if got, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.2.argv")); !ok || !reflect.DeepEqual(got, wantActivate) {
		t.Errorf("S45: sudo.2 argv = %q (ran=%v), want %q", got, ok, wantActivate)
	}
	if _, ok := readArgvFile(t, filepath.Join(wsRoot, "sudo.3.argv")); ok {
		t.Error("S45: a third numbered sudo ran")
	}
	// The build ran as the invoking user, not root.
	uid, ok := readArgvFile(t, filepath.Join(wsRoot, "build.uid"))
	if !ok || len(uid) != 1 || uid[0] != strconv.Itoa(os.Getuid()) {
		t.Errorf("S45: build uid = %q (ran=%v), want %d", uid, ok, os.Getuid())
	}
}
