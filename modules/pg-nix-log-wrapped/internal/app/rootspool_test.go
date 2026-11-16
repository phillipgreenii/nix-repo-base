package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/config"
	"github.com/phillipgreenii/nix-repo-base/modules/pg-nix-log-wrapped/internal/fakecmd"
	"github.com/phillipgreenii/x/otelsetup/spool"
)

// newSpoolRootRig is a rig acting as root: the root spool and log directories are
// temp stand-ins, and ownership comes from a seam instead of a real chown.
func newSpoolRootRig(t *testing.T, uid int) *rig {
	t.Helper()
	r := newRig(t, fakecmd.Spec{})
	r.d.Euid = 0
	base := t.TempDir()
	r.d.RootLogDir = filepath.Join(base, "log")
	r.d.RootSpoolDir = filepath.Join(base, "spool")
	r.d.SpoolOwner = func(os.FileInfo) (int, bool) { return uid, true }
	return r
}

func TestRootIgnoresTheUserSpoolFlagValue(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	userSpool := filepath.Join(r.home, "chosen-by-user")
	r.run("--otel-spool-dir", userSpool, "fake-nix")
	if _, err := os.Stat(userSpool); err == nil {
		t.Error("a root wrapper used the user-chosen spool directory")
	}
	if _, err := os.Stat(r.spool); err == nil {
		t.Error("a root wrapper used the XDG spool directory")
	}
	// The seam says the stand-in is root-owned, so root created its own spool
	// with the readable mode (the umask must not narrow it).
	fi, err := os.Stat(r.d.RootSpoolDir)
	if err != nil {
		t.Fatalf("root spool not prepared: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("root spool mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestRootTelemetryIsOffWithoutTheFlagOrLegacyInput(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	r.run("fake-nix")
	if r.mem.calls != 0 || len(r.execs) != 1 {
		t.Errorf("calls=%d execs=%d", r.mem.calls, len(r.execs))
	}
	if _, err := os.Stat(r.d.RootSpoolDir); err == nil {
		t.Error("root spool created although nothing asked for telemetry")
	}
}

func TestRootFailsOpenOnASymlinkedSpool(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	target := t.TempDir()
	if err := os.Symlink(target, r.d.RootSpoolDir); err != nil {
		t.Fatal(err)
	}
	if code := r.run("--otel-spool-dir", "x", "fake-nix"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if r.mem.calls != 0 || len(r.execs) != 1 || r.stderr.Len() != 0 {
		t.Errorf("calls=%d execs=%d stderr=%q", r.mem.calls, len(r.execs), r.stderr)
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Errorf("root wrote through the symlink: %v", ents)
	}
}

func TestRootFailsOpenOnANonRootOwnedSpool(t *testing.T) {
	r := newSpoolRootRig(t, 1234) // the seam: the directory belongs to uid 1234
	if err := os.Mkdir(r.d.RootSpoolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.run("--otel-spool-dir", "x", "fake-nix")
	if r.mem.calls != 0 || len(r.execs) != 1 || r.stderr.Len() != 0 {
		t.Errorf("calls=%d execs=%d stderr=%q", r.mem.calls, len(r.execs), r.stderr)
	}
	if ents, _ := os.ReadDir(r.d.RootSpoolDir); len(ents) != 0 {
		t.Errorf("root wrote into a directory it does not own: %v", ents)
	}
}

func TestRootCheckUsesTheRootSpool(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	r.run("--check", "--otel-spool-dir", "x")
	if want := "spool-dir: " + r.d.RootSpoolDir + " (from root) (writable: yes)"; !strings.Contains(r.stdout.String(), want) {
		t.Errorf("missing %q in\n%s", want, r.stdout)
	}
}

func TestSpoolRolesCarryTheContractModes(t *testing.T) {
	for _, c := range []struct {
		role      spool.Role
		dir, file os.FileMode
	}{{spool.RoleRoot, 0o755, 0o644}, {spool.RoleUser, 0o700, 0o600}} {
		if c.role.DirMode() != c.dir || c.role.FileMode() != c.file {
			t.Errorf("%v: dir %v file %v", c.role, c.role.DirMode(), c.role.FileMode())
		}
	}
}

func TestRootSessionRequestsTheRootRole(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	// The stand-in log directory is not root-owned for a non-root test
	// process, so the run fails open after the spool is prepared; the role is
	// asserted on the resolution instead.
	res := config.Resolve(config.Inputs{Opts: config.Options{RootSpool: true}, Euid: 0, RootSpoolDir: r.d.RootSpoolDir})
	if !res.Root || res.SpoolDir != r.d.RootSpoolDir {
		t.Errorf("%+v", res)
	}
}

func TestDeprecatedEndpointFlagPrintsExactlyOneNoticeAndDoesNothingElse(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.telemetryOff(t)
	before := append([]string(nil), r.d.Environ...)
	if code := r.run("--otlp-endpoint", "http://127.0.0.1:4318", "fake-nix", "build"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if got := r.stderr.String(); got != config.DeprecatedOTLPNotice || strings.Count(got, "\n") != 1 {
		t.Errorf("stderr = %q", got)
	}
	// Nothing else: telemetry stays off, CMD is exec'd untouched.
	if r.mem.calls != 0 || len(r.execs) != 1 || strings.Join(r.execs[0].env, "\x00") != strings.Join(before, "\x00") {
		t.Errorf("calls=%d execs=%+v", r.mem.calls, r.execs)
	}
	// And it does not turn telemetry on for a user run.
	if _, err := os.Stat(r.spool); err == nil {
		t.Error("the deprecated flag created a spool")
	}
}

func TestDeprecatedEndpointFlagDoesNotEnableRootTelemetry(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	r.run("--otlp-endpoint", "http://x", "fake-nix")
	if r.mem.calls != 0 {
		t.Error("the deprecated flag enabled root telemetry")
	}
	if _, err := os.Stat(r.d.RootSpoolDir); err == nil {
		t.Error("root spool created from the deprecated flag")
	}
}

func TestNoNoticeWithoutTheDeprecatedFlag(t *testing.T) {
	r := newRig(t, fakecmd.Spec{})
	r.run("fake-nix")
	if r.stderr.Len() != 0 {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestLegacyRootEnableInputStillEnablesRootTelemetry(t *testing.T) {
	r := newSpoolRootRig(t, 0)
	r.d.Args = []string{"fake-nix"}
	// A caller that still sets OTLPEndpoint keeps enabling root telemetry.
	rr := &runner{Deps: r.d, getenv: envLookup(r.d.Environ)}
	res := config.Resolve(config.Inputs{Opts: config.Options{OTLPEndpoint: "http://legacy"}, Euid: 0, RootSpoolDir: rr.RootSpoolDir})
	if res.SpoolDir != rr.RootSpoolDir || !res.Root {
		t.Errorf("%+v", res)
	}
}
