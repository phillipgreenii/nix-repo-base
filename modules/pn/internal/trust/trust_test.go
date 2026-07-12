package trust

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, configFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestTOFU_RoundTrip covers the trust-on-first-use lifecycle: unknown → not
// allowed; Allow → allowed; editing the TOML re-blocks (content-hash) with a
// distinct error; Deny revokes and is a no-op when already absent.
func TestTOFU_RoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeConfig(t, root, "[workspace]\nid='x'\n")

	if ok, err := IsAllowed(root); err != nil || ok {
		t.Fatalf("unknown root: IsAllowed=%v err=%v; want false,nil", ok, err)
	}
	if err := EnsureAllowed(root); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("EnsureAllowed unknown: want ErrUntrusted; got %v", err)
	} else if !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("never-trusted error should say 'not trusted'; got %v", err)
	}

	if err := Allow(root); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if ok, err := IsAllowed(root); err != nil || !ok {
		t.Fatalf("after Allow: IsAllowed=%v err=%v; want true,nil", ok, err)
	}
	if err := EnsureAllowed(root); err != nil {
		t.Fatalf("EnsureAllowed after Allow: %v", err)
	}

	// Edit the TOML → re-blocked with a distinct "changed" error.
	writeConfig(t, root, "[workspace]\nid='x'\n[[hooks]]\nwhen=['pre-status']\nrun=['evil']\n")
	if ok, err := IsAllowed(root); err != nil || ok {
		t.Fatalf("after edit: IsAllowed=%v err=%v; want false,nil", ok, err)
	}
	err := EnsureAllowed(root)
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("EnsureAllowed after edit: want ErrUntrusted; got %v", err)
	}
	if !strings.Contains(err.Error(), "changed since") {
		t.Errorf("changed-config error should be distinct from never-trusted; got %v", err)
	}

	// Deny revokes; a second Deny is a no-op.
	if err := Allow(root); err != nil {
		t.Fatalf("re-Allow: %v", err)
	}
	if err := Deny(root); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if ok, _ := IsAllowed(root); ok {
		t.Fatalf("after Deny: still allowed")
	}
	if err := Deny(root); err != nil {
		t.Fatalf("Deny of missing record must be a no-op; got %v", err)
	}
}

// TestPermissions asserts the state dir is 0700 and the record file 0600 so a
// co-tenant cannot pre-seed or read a trust record.
func TestPermissions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeConfig(t, root, "[workspace]\nid='x'\n")
	if err := Allow(root); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	di, err := os.Stat(stateDir())
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("state dir perm = %o; want 700", di.Mode().Perm())
	}
	rp, err := recordPath(root)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(rp)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record perm = %o; want 600", fi.Mode().Perm())
	}
}
