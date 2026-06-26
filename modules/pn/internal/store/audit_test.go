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

// scriptSystemProfile adds sudo nix-env --list-generations + nix path-info -S
// for /nix/var/nix/profiles/system.
func scriptSystemProfile(f *exec.FakeRunner, genStdout []byte, pathInfoStdout []byte) {
	f.AddResponse("sudo",
		[]string{"nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
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
		"Volume used: 12.0 GB",
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
	// deadPathsSize: sudo nix-store --gc --print-dead + nix path-info -S <paths>
	deadPath := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-dead"
	f.AddResponse("sudo", []string{"nix-store", "--gc", "--print-dead"},
		exec.Result{Stdout: []byte(deadPath + "\n")}, nil)
	f.AddResponse("nix", []string{"path-info", "-S", deadPath},
		exec.Result{Stdout: []byte(deadPath + " 524288\n")}, nil)

	var buf, errBuf bytes.Buffer
	s := NewWithEnv(f, Env{Home: home})
	if err := s.Audit(context.Background(), &buf, &errBuf, AuditOptions{Full: true}); err != nil {
		t.Fatalf("Audit(Full): %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Reclaimable (dead paths):") {
		t.Errorf("--full must include Reclaimable; output:\n%s", out)
	}
}

func TestAudit_ToleratesProfileError(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// system nix-env fails
	f.AddResponse("sudo",
		[]string{"nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
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

// TestAudit_GoldenOutput locks the exact byte output for a minimal fixture:
// one system generation (non-current + current), empty HM/User/DevboxGlobal/DevboxProjects,
// and the Nix Store section.  This test catches any whitespace drift in generation
// lines (trailing space for non-current), section headers, and block shape.
func TestAudit_GoldenOutput(t *testing.T) {
	noSymlinkResolution(t)
	home := t.TempDir()
	f := exec.NewFakeRunner()

	// system profile: two generations — gen 1 non-current, gen 2 current
	f.AddResponse("sudo",
		[]string{"nix-env", "--profile", "/nix/var/nix/profiles/system", "--list-generations"},
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
		"    Closure size: 1.0 GB\n" +
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
		"Volume used: 10.0 GB\n"

	got := buf.String()
	if got != want {
		t.Errorf("golden output mismatch.\nWANT:\n%q\n\nGOT:\n%q", want, got)
	}
}
