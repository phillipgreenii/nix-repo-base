package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const liveTOML = `[workspace]
name = ''
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'

[repos.phillipg-nix-repo-base]
url = 'git@github.com:phillipgreenii/nix-repo-base.git'
branch = 'main'

[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:phillipgziprecruiter/phillipg_mbp.git'
branch = 'main'

[hooks.apply]
post = ['pb gate check']
`

// run against a root whose file already matches → exit 0, no "enforced" line.
func TestRun_NoOpWhenCorrect(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pn-workspace.toml"), []byte(liveTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0. output: %s", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("enforced")) {
		t.Errorf("printed an 'enforced' line on a no-op: %q", out.String())
	}
}

// run against a root whose id is wrong → rewrites and prints an "enforced" line.
func TestRun_EnforcesAndReports(t *testing.T) {
	dir := t.TempDir()
	wrong := `[workspace]
id = ''

[repos.r]
url = 'git@github.com:x/y.git'
`
	p := filepath.Join(dir, "pn-workspace.toml")
	if err := os.WriteFile(p, []byte(wrong), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0. output: %s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("enforced")) {
		t.Errorf("expected an 'enforced' line, got: %q", out.String())
	}
	data, _ := os.ReadFile(p)
	if !bytes.Contains(data, []byte("id = 'phillipg-mbp'")) {
		t.Errorf("id not written: %s", data)
	}
}

// Absent file → exit 0, no-op (pn workspace init owns creation).
func TestRun_AbsentFileNoOp(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	code := run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0 for absent file. output: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pn-workspace.toml")); !os.IsNotExist(err) {
		t.Errorf("created a file for an absent root; want no-op")
	}
}

// A bad id → non-zero exit.
func TestRun_RejectsBadID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pn-workspace.toml"), []byte(liveTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--root", dir, "--id", "BAD_ID", "--apply-post", "pb gate check"}, &out)
	if code == 0 {
		t.Errorf("exit code = 0; want non-zero for a bad id")
	}
}

// Missing required flags → non-zero exit.
func TestRun_MissingFlags(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"--root", t.TempDir()}, &out)
	if code == 0 {
		t.Errorf("exit code = 0; want non-zero when --id/--apply-post missing")
	}
}
