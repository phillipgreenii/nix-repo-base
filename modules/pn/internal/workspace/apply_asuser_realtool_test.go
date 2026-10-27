package workspace

import (
	"bytes"
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pnexec "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// Real-tool tests (V-3) of the two root steps' dependence on the installed
// darwin-rebuild script (bead pg2-sdbhh4.9, ADR 0038). They only READ the script
// and run its `--help` path, which exits at the usage text before any nix, id or
// filesystem work: nothing here evaluates, builds, activates or uses sudo.
// They skip with a message where darwin-rebuild is not installed (a Linux
// machine, the nix check sandbox).

// installedDarwinRebuild returns the resolved path of the installed script, or
// skips the test.
func installedDarwinRebuild(t *testing.T) string {
	t.Helper()
	p, err := osexec.LookPath("darwin-rebuild")
	if err != nil {
		t.Skip("SKIP: darwin-rebuild is not installed on this machine; the drift guard and the real-tool probe test need the real script")
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Skipf("SKIP: cannot resolve %s: %v", p, err)
	}
	return real
}

// The root steps rely on four facts about the script; if a nix-darwin update
// changes one, this fails BEFORE an apply discovers it as root. The argv of the
// root steps themselves is otherwise checked only against fakes.
func TestDarwinRebuildScript_DriftGuard(t *testing.T) {
	script := installedDarwinRebuild(t)
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	t.Run("activate is in the action list", func(t *testing.T) {
		if !regexp.MustCompile(`(?m)^\s*edit\|switch\|activate\|build\|check\|changelog\)`).MatchString(src) {
			t.Error("the `edit|switch|activate|build|check|changelog)` action case is gone or changed")
		}
	})
	t.Run("the activate branch derives systemConfig from the invoked path", func(t *testing.T) {
		re := regexp.MustCompile(`(?s)if \[ "\$action" = activate \]; then\s*\n\s*systemConfig=\$\(readlink -f "\$\{0%\*/sw/bin/darwin-rebuild\}"\)\s*\n\s*fi`)
		if !re.MatchString(src) {
			t.Error(`the [ "$action" = activate ] branch with systemConfig=$(readlink -f "${0%*/sw/bin/darwin-rebuild}") is gone or changed: pn runs <out>/sw/bin/darwin-rebuild activate BY ITS INVOKED PATH because of it`)
		}
	})
	t.Run("only switch sets the profile and activate runs the activate script", func(t *testing.T) {
		if !regexp.MustCompile(`(?s)if \[ "\$action" = switch \]; then\s*\n\s*nix-env -p "\$profile" --set "\$systemConfig"\s*\n\s*fi`).MatchString(src) {
			t.Error(`nix-env -p "$profile" --set "$systemConfig" is no longer guarded by [ "$action" = switch ]: activate would set the profile too, or switch would not`)
		}
		if !regexp.MustCompile(`(?s)\[ "\$action" = switch \] \|\| \[ "\$action" = activate \].*"\$systemConfig/activate"`).MatchString(src) {
			t.Error(`the switch/activate/rollback branch that runs "$systemConfig/activate" is gone or changed`)
		}
	})
	t.Run("the root check is still there", func(t *testing.T) {
		if !strings.Contains(src, `^switch|activate|rollback|check$`) || !strings.Contains(src, `$(id -u) -ne 0`) {
			t.Error("the `^switch|activate|rollback|check$ && $(id -u) -ne 0` root check is gone or changed")
		}
	})
}

// The probe parser accepts the real usage of the installed script.
func TestRealProbe_ParserAcceptsTheInstalledScriptsUsage(t *testing.T) {
	script := installedDarwinRebuild(t)
	var stdout, stderr bytes.Buffer
	cmd := osexec.Command(script, "--help")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*osexec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if perr := parseDarwinRebuildUsage(code, stdout.Bytes(), stderr.Bytes()); perr != nil {
		t.Errorf("the probe parser rejects the installed script's --help (exit %d): %v\nstderr: %s", code, perr, stderr.String())
	}
}

// A fixture "out" directory whose sw/bin/darwin-rebuild is a symlink to the
// installed script: the validation accepts the INVOKED path (its realpath is not
// under out), and the real probe, run through the real runner on that invoked
// path, passes.
func TestRealProbe_FixtureOutDirSymlinkedToTheInstalledScript(t *testing.T) {
	script := installedDarwinRebuild(t)
	store := t.TempDir()
	out := filepath.Join(store, fakeHash+"-darwin-system-fixture")
	if err := os.MkdirAll(filepath.Join(out, "sw", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "activate"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(script, filepath.Join(out, "sw", "bin", "darwin-rebuild")); err != nil {
		t.Fatal(err)
	}
	if err := validateBuildOut(out, store); err != nil {
		t.Fatalf("validateBuildOut: %v", err)
	}
	ws := &Workspace{runner: pnexec.NewRealRunner()}
	res, reason := ws.probeActivate(context.Background(), t.TempDir(), out)
	if reason != "" {
		t.Errorf("the real probe failed: %s (exit %d, stderr %q)", reason, res.ExitCode, res.Stderr)
	}
	if res.ExitCode != 1 {
		t.Errorf("probe exit = %d, want 1", res.ExitCode)
	}
}
