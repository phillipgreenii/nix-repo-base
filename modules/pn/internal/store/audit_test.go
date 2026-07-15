package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

// noSymlinkResolution overrides the evalSymlinks seam so that profile paths
// are never resolved. This ensures that FakeRunner responses can be scripted
// against raw profile paths regardless of whether /nix/var/nix/profiles/system
// happens to exist on the host machine.
func noSymlinkResolution(t *testing.T) {
	t.Helper()
	orig := evalSymlinks
	evalSymlinks = func(path string) (string, error) { return "", errors.New("test: symlink resolution disabled") }
	t.Cleanup(func() { evalSymlinks = orig })
}

// scriptStoreSize adds the two FakeRunner responses that storeSize requires:
// df + diskutil.  device is the block device returned by df.
func scriptStoreSize(f *exec.FakeRunner, device, diskutilLine string) {
	f.AddResponse("df", []string{"/nix/store"}, exec.Result{
		Stdout: []byte("Filesystem 1K-blocks Used Available Use% Mounted on\n" + device + " 1 1 1 1% /nix/store\n"),
	}, nil)
	f.AddResponse("diskutil", []string{"info", device}, exec.Result{
		Stdout: []byte(diskutilLine + "\n"),
	}, nil)
}

// scriptSystemProfile adds sudo -n nix-env --list-generations + nix path-info -S
// for /nix/var/nix/profiles/system. The audit lists the system profile
// non-interactively (`sudo -n`, pg2-ssp8), hence the leading -n.
func scriptSystemProfile(f *exec.FakeRunner, genStdout []byte, pathInfoStdout []byte) {
	f.AddResponse("sudo",
		[]string{"-n", "nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
		exec.Result{Stdout: genStdout}, nil)
	// profileClosureSize calls EvalSymlinks which fails in tests → falls back to raw path.
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"},
		exec.Result{Stdout: pathInfoStdout}, nil)
}

func TestAudit_EmitsSectionsAndStoreSize(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	scriptStoreSize(f, "/dev/disk1", "Volume Used Space: 12.0 GB (12884901888 Bytes)")
	scriptSystemProfile(f,
		[]byte("1 2024-01-01 12:00:00 (current)\n"),
		[]byte("/nix/var/nix/profiles/system 1048576\n"))

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"=== System Profiles ===",
		"=== Home Manager ===",
		"=== Nix Store ===",
		"Volume used: 12.0 GiB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Reclaimable") {
		t.Error("non-full audit must not show Reclaimable")
	}
}

func TestAudit_FullShowsReclaimable(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	scriptStoreSize(f, "/dev/disk2", "Volume Used Space: 8.5 GB (9126805504 Bytes)")
	scriptSystemProfile(f,
		[]byte("1 2024-01-01 12:00:00 (current)\n"),
		[]byte("/nix/var/nix/profiles/system 2097152\n"))
	// deadPathsSize: sudo -n nix-store --gc --print-dead + nix path-info -S <paths>
	// (audit --full lists dead paths non-interactively, pg2-ssp8)
	deadPath := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-dead"
	f.AddResponse("sudo", []string{"-n", "nix-store", "--gc", "--print-dead"},
		exec.Result{Stdout: []byte(deadPath + "\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", deadPath},
		exec.Result{Stdout: []byte(deadPath + " 524288\n")}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{Full: true}); err != nil {
		t.Fatalf("Audit(Full): %v", err)
	}
	out := buf.String()
	// deadPath size is 524288 bytes = 512.0 KiB (< 1 MiB threshold)
	wantLine := "Reclaimable (dead paths): 512.0 KiB"
	if !strings.Contains(out, wantLine) {
		t.Errorf("--full must include exact line %q; output:\n%s", wantLine, out)
	}
}

// TestAudit_FullNonInteractiveSudoDoesNotBlock guards the pg2-ssp8 --full gap:
// deadPathsSize also runs sudo, so `audit --full` must list dead paths with
// `sudo -n` and degrade gracefully when that fails (no prompt, no hang, no
// abort). Both sudo call sites (system profile + dead paths) fail fast here;
// the audit must still complete and render every section, with the Reclaimable
// line present but reporting "unknown".
func TestAudit_FullNonInteractiveSudoDoesNotBlock(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// system profile sudo -n fails fast.
	f.AddResponse("sudo",
		[]string{"-n", "nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
		exec.Result{ExitCode: 1}, errors.New("sudo: a password is required"))
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"},
		exec.Result{ExitCode: 1}, errors.New("path-info failed"))
	// dead-paths sudo -n also fails fast — the --full-only call this test guards.
	f.AddResponse("sudo", []string{"-n", "nix-store", "--gc", "--print-dead"},
		exec.Result{ExitCode: 1}, errors.New("sudo: a password is required"))
	scriptStoreSize(f, "/dev/disk5", "Volume Used Space: 3.0 GB (3221225472 Bytes)")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{Full: true}); err != nil {
		t.Fatalf("Audit(Full) must not error when sudo -n fails: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"=== Nix Store ===",
		"Volume used: 3.0 GiB",
		"Reclaimable (dead paths): unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit --full must degrade gracefully; missing %q in:\n%s", want, out)
		}
	}
	// Every sudo invocation from --full audit must be non-interactive (-n).
	var sudoCalls int
	for _, c := range f.Calls() {
		if c.Name != "sudo" {
			continue
		}
		sudoCalls++
		if len(c.Args) == 0 || c.Args[0] != "-n" {
			t.Errorf("sudo must be invoked with -n; got args %v", c.Args)
		}
	}
	if sudoCalls != 2 {
		t.Errorf("expected 2 sudo calls (system profile + dead paths), got %d", sudoCalls)
	}
}

func TestAudit_ToleratesProfileError(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// system nix-env fails (audit uses `sudo -n`, pg2-ssp8)
	f.AddResponse("sudo",
		[]string{"-n", "nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
		exec.Result{ExitCode: 1}, errors.New("nix-env failed"))
	// profileClosureSize also fails
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"},
		exec.Result{ExitCode: 1}, errors.New("path-info failed"))

	scriptStoreSize(f, "/dev/disk3", "Volume Used Space: 5.0 GB (5368709120 Bytes)")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{})
	if err != nil {
		t.Fatalf("Audit must return nil on per-profile errors; got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Profile: /nix/var/nix/profiles/system") {
		t.Errorf("output must still contain profile path; got:\n%s", out)
	}
	if !strings.Contains(out, "Closure size: unknown") {
		t.Errorf("output must show 'Closure size: unknown' on error; got:\n%s", out)
	}
	if !strings.Contains(out, "=== Nix Store ===") {
		t.Errorf("output must continue to Nix Store section; got:\n%s", out)
	}
}

// TestAudit_NonInteractiveSudoDoesNotBlock is the pg2-ssp8 regression guard.
//
// The read-only `audit` must never block on a sudo password prompt. It lists
// the system profile's generations with `sudo -n` (non-interactive): sudo(8)
// guarantees -n never prompts on /dev/tty — it fails fast when no credentials
// are cached instead of hanging. This test scripts that fail-fast response
// (mirroring the bead's fail-fast `sudo` shim) and asserts:
//
//	(a) Audit tolerates the error and still renders the sudo-free sections,
//	    especially the Nix Store "Volume used" line, and
//	(b) the sudo invocation carries -n as its first argument (the property that
//	    makes it non-blocking; before the fix sudo was invoked without -n and
//	    would prompt/hang in a non-interactive context).
func TestAudit_NonInteractiveSudoDoesNotBlock(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// sudo -n fails fast (exit 1) — simulates no cached credentials WITHOUT a
	// prompt, exactly as the bead's fail-fast `sudo` shim would.
	f.AddResponse("sudo",
		[]string{"-n", "nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
		exec.Result{ExitCode: 1}, errors.New("sudo: a password is required"))
	// Closure size also fails → "unknown" (still no sudo prompt on this path).
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"},
		exec.Result{ExitCode: 1}, errors.New("path-info failed"))
	scriptStoreSize(f, "/dev/disk4", "Volume Used Space: 7.0 GB (7516192768 Bytes)")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{}); err != nil {
		t.Fatalf("Audit must not error when sudo -n fails: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"=== System Profiles ===",
		"Profile: /nix/var/nix/profiles/system",
		"=== Nix Store ===",
		"Volume used: 7.0 GiB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("audit must render sudo-free sections despite sudo failure; missing %q in:\n%s", want, out)
		}
	}
	// The fix: sudo MUST be invoked non-interactively (-n) so it can never
	// block. sudo(8): with -n sudo never prompts and fails fast instead.
	var sawSudo bool
	for _, c := range f.Calls() {
		if c.Name != "sudo" {
			continue
		}
		sawSudo = true
		if len(c.Args) == 0 || c.Args[0] != "-n" {
			t.Errorf("sudo must be invoked with -n (non-interactive) so audit never blocks; got args %v", c.Args)
		}
	}
	if !sawSudo {
		t.Error("expected audit to invoke sudo for the system profile")
	}
}

// TestAudit_GoldenOutput locks the exact byte output for a minimal fixture:
// one system generation (non-current + current), empty HM/User/DevboxGlobal/DevboxProjects,
// and the Nix Store section.  This test catches any whitespace drift in generation
// lines (trailing space for non-current), section headers, and block shape.
func TestAudit_GoldenOutput(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// system profile: two generations — gen 1 non-current, gen 2 current
	// (audit uses `sudo -n`, pg2-ssp8)
	f.AddResponse("sudo",
		[]string{"-n", "nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
		exec.Result{Stdout: []byte("   1   2024-01-01 10:00:00\n   2   2024-06-01 12:00:00 (current)\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", "/nix/var/nix/profiles/system"},
		exec.Result{Stdout: []byte("/nix/var/nix/profiles/system 1073741824\n")}, nil)

	scriptStoreSize(f, "/dev/disk9", "Volume Used Space: 10.0 GB (10737418240 Bytes)")

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{}); err != nil {
		t.Fatalf("Audit: %v", err)
	}

	// Build expected string exactly as the bash would produce it.
	// Note: non-current generation line has a trailing space (awk prints empty
	// third field with the space separator).
	want := "" +
		"=== System Profiles ===\n" +
		"  system:\n" +
		"    Profile: /nix/var/nix/profiles/system\n" +
		"    1 2024-01-01 \n" + // trailing space — non-current
		"    2 2024-06-01 current\n" +
		"    Closure size: 1.0 GiB\n" +
		"\n" +
		"=== Home Manager ===\n" +
		"  (not installed)\n" +
		"\n" +
		"=== User Profiles ===\n" +
		"=== Devbox Global ===\n" +
		"  (not installed)\n" +
		"\n" +
		"=== Devbox Projects ===\n" +
		"  (no search dirs configured)\n" +
		"\n" +
		"=== Nix Store ===\n" +
		"Volume used: 10.0 GiB\n"

	got := buf.String()
	if got != want {
		t.Errorf("golden output mismatch.\nWANT:\n%q\n\nGOT:\n%q", want, got)
	}
}
