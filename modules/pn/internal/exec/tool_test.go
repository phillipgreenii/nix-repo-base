package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetry/telemetrytest"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/telemetrycfg"
)

// pathLacking points PATH at an empty directory, so no real darwin-rebuild (or
// anything else) is found there.
func pathLacking(t *testing.T) string {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	return empty
}

func TestResolveTool_OnPathNeedsNothing(t *testing.T) {
	dir := t.TempDir()
	fakeCmd(t, dir, "darwin-rebuild")
	t.Setenv("PATH", dir)
	env, err := ResolveTool("darwin-rebuild", []string{"/does/not/exist"})
	if err != nil || env != nil {
		t.Fatalf("ResolveTool = %v, %v; want nil, nil", env, err)
	}
}

func TestResolveTool_FallbackDirAppendsToPath(t *testing.T) {
	empty := pathLacking(t)
	profile := t.TempDir()
	fakeCmd(t, profile, "darwin-rebuild")
	env, err := ResolveTool("darwin-rebuild", []string{"/does/not/exist", profile})
	if err != nil {
		t.Fatal(err)
	}
	want := empty + string(os.PathListSeparator) + profile
	if env["PATH"] != want || len(env) != 1 {
		t.Fatalf("env = %v, want PATH=%q only", env, want)
	}
}

func TestResolveTool_NonExecutableFallbackIsIgnored(t *testing.T) {
	pathLacking(t)
	profile := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "darwin-rebuild"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTool("darwin-rebuild", []string{profile}); !IsToolNotFound(err) {
		t.Fatalf("err = %v, want ToolNotFoundError", err)
	}
}

func TestResolveTool_NotFoundAnywhereNamesTheDirs(t *testing.T) {
	pathLacking(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	env, err := ResolveTool("darwin-rebuild", dirs)
	var tnf *ToolNotFoundError
	if !errors.As(err, &tnf) || env != nil {
		t.Fatalf("ResolveTool = %v, %v; want ToolNotFoundError", env, err)
	}
	msg := err.Error()
	for _, want := range append([]string{"darwin-rebuild not found on PATH", "also tried", "system profile on PATH"}, dirs...) {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

func TestResolveTool_PathQualifiedNameIsLeftAlone(t *testing.T) {
	pathLacking(t)
	if env, err := ResolveTool("/abs/darwin-rebuild", nil); env != nil || err != nil {
		t.Fatalf("ResolveTool = %v, %v; want nil, nil", env, err)
	}
}

// TestSystemProfileBinDirs pins the two directories; both exist on a nix-darwin
// machine (checked 2026-10-27 on the dev machine, bead pg2-sdbhh4.12).
func TestSystemProfileBinDirs(t *testing.T) {
	want := []string{"/run/current-system/sw/bin", "/nix/var/nix/profiles/system/sw/bin"}
	if strings.Join(SystemProfileBinDirs, " ") != strings.Join(want, " ") {
		t.Fatalf("SystemProfileBinDirs = %v, want %v", SystemProfileBinDirs, want)
	}
}

// TestToolNotFoundExitCodeIsDistinct: 0/1 are generic, 2 is usage, 127 is the
// shell's "command not found" that this guard replaces.
func TestToolNotFoundExitCodeIsDistinct(t *testing.T) {
	switch ToolNotFoundExitCode {
	case 0, 1, 2, 126, 127:
		t.Fatalf("ToolNotFoundExitCode = %d collides with a conventional status", ToolNotFoundExitCode)
	}
}

// TestRealRunner_ResolvesBareNameAgainstEnvPATH: the real runner looks the bare
// name up in the PATH it is GIVEN, not only the parent's.
func TestRealRunner_ResolvesBareNameAgainstEnvPATH(t *testing.T) {
	pathLacking(t)
	profile := t.TempDir()
	fakeCmd(t, profile, "darwin-rebuild")
	argvOut := filepath.Join(t.TempDir(), "argv")
	env, err := ResolveTool("darwin-rebuild", []string{profile})
	if err != nil {
		t.Fatal(err)
	}
	env["ARGV_OUT"] = argvOut
	var out bytes.Buffer
	if _, err := (&realRunner{}).Run(context.Background(), "darwin-rebuild", []string{"build"}, RunOptions{Env: env, Stdout: &out}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "ran-darwin-rebuild\n" {
		t.Errorf("stdout = %q", out.String())
	}
	// Without the overlay the same call must still fail to start.
	if _, err := (&realRunner{}).Run(context.Background(), "darwin-rebuild", []string{"build"}, RunOptions{}); err == nil {
		t.Error("Run without the PATH overlay unexpectedly succeeded")
	}
}

// TestWrap_FallbackToolStillRunsWrapped (acceptance criterion): with a PATH that
// lacks darwin-rebuild, the tool is found via the fallback dir AND still runs
// through the wrapper, with the BARE name in the wrapped argv (an absolute path
// would run unwrapped).
func TestWrap_FallbackToolStillRunsWrapped(t *testing.T) {
	pathLacking(t)
	profile := t.TempDir()
	fakeCmd(t, profile, "darwin-rebuild")
	wrapper := fakeWrapper(t, t.TempDir())
	work := t.TempDir()
	argvOut, wrapperOut := filepath.Join(work, "argv"), filepath.Join(work, "wrapper-argv")

	env, err := ResolveTool("darwin-rebuild", []string{profile})
	if err != nil {
		t.Fatal(err)
	}
	env["ARGV_OUT"], env["WRAPPER_OUT"] = argvOut, wrapperOut

	tel, _ := telemetrytest.New()
	res := telemetrycfg.Resolution{Enabled: true, Endpoint: testEndpoint, WrapperPath: wrapper}
	ctx := telemetry.WithTelemetry(context.Background(), tel)
	ctx = telemetrycfg.WithRunState(ctx, telemetrycfg.NewRunState(res))
	var out bytes.Buffer
	if _, err := NewRealRunner().Run(ctx, "darwin-rebuild", []string{"build", "--flake", "/x#h"}, RunOptions{WrapNix: true, Stdout: &out, Env: env}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	wargv, err := os.ReadFile(wrapperOut)
	if err != nil {
		t.Fatalf("the wrapper did not run: %v", err)
	}
	if !bytes.Contains(wargv, []byte("\x00--\x00darwin-rebuild\x00build\x00--flake\x00/x#h\x00")) {
		t.Errorf("wrapper argv = %q; want the bare name darwin-rebuild after --", wargv)
	}
	if out.String() != "ran-darwin-rebuild\n" {
		t.Errorf("the tool did not run under the wrapper: stdout %q", out.String())
	}
}

// TestWrap_RewriteStillAppliesWithPATHOverlay: the decorator rewrite is
// unchanged by an Env overlay, and the overlay reaches the inner runner.
func TestWrap_RewriteStillAppliesWithPATHOverlay(t *testing.T) {
	e := newWrapEnv(t, "/wrapper/pg-nix-log-wrapped")
	env := map[string]string{"PATH": "/bin:/run/current-system/sw/bin"}
	if _, err := e.r.Run(e.ctx, "darwin-rebuild", []string{"build"}, RunOptions{WrapNix: true, Env: env}); err != nil {
		t.Fatal(err)
	}
	c := e.only(t)
	if c.Name != "/wrapper/pg-nix-log-wrapped" || !containsArg(c.Args, "darwin-rebuild") {
		t.Errorf("not rewritten: %s %v", c.Name, c.Args)
	}
	if c.Opts.Env["PATH"] != env["PATH"] {
		t.Errorf("Env not forwarded: %v", c.Opts.Env)
	}
}
