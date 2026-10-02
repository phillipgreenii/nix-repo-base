package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func script(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLookPathSkipsSelfByInode(t *testing.T) {
	shadow, real := t.TempDir(), t.TempDir()
	self := script(t, t.TempDir(), "wrapper", 0o755)
	// A PATH entry that is the wrapper itself (a symlink named like CMD) comes
	// first; the real command comes later.
	if err := os.Symlink(self, filepath.Join(shadow, "nix")); err != nil {
		t.Fatal(err)
	}
	want := script(t, real, "nix", 0o755)
	fi, _ := os.Stat(self)
	got, err := LookPath("nix", shadow+string(os.PathListSeparator)+real, fi)
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	// Without the self-skip the shadow would win (proves the test bites).
	got, _ = LookPath("nix", shadow+string(os.PathListSeparator)+real, nil)
	if got != filepath.Join(shadow, "nix") {
		t.Fatalf("control: got %q", got)
	}
	// Only the shadow on PATH: not found, not recursion.
	var nf *NotFoundError
	if _, err := LookPath("nix", shadow, fi); !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
}

func TestLookPathMessagesMatchExec(t *testing.T) {
	_, err := LookPath("definitely-missing-cmd", t.TempDir(), nil)
	if err == nil || err.Error() != `exec: "definitely-missing-cmd": executable file not found in $PATH` {
		t.Errorf("bare-name message = %v", err)
	}
	_, err = LookPath("/no/such/dir/cmd", "", nil)
	var nf *NotFoundError
	if !errors.As(err, &nf) || err.Error() != "fork/exec /no/such/dir/cmd: no such file or directory" {
		t.Errorf("slash message = %v", err)
	}
	noexec := script(t, t.TempDir(), "x", 0o644)
	var pe *PermissionError
	if _, err := LookPath(noexec, "", nil); !errors.As(err, &pe) {
		t.Errorf("non-executable: %v", err)
	}
}

func TestLookPathSkipsDirsAndNonExecutables(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(a, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	script(t, a, "tool2", 0o644)
	want := script(t, b, "tool", 0o755)
	got, err := LookPath("tool", a+string(os.PathListSeparator)+b, nil)
	if err != nil || got != want {
		t.Errorf("got %q, %v", got, err)
	}
}
