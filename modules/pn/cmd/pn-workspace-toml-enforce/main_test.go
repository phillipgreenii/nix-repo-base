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

[[hooks]]
when = ['post-apply', 'post-upgrade']
run = ['pb gate check']
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

// --build-command / --apply-command enforce those keys, report a change, and
// leave workspace.terminal + [repos.*] intact (bead pg2-k43p.8).
func TestRun_EnforcesBuildAndApplyCommands(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pn-workspace.toml")
	// liveTOML has id/terminal/apply-post but no command keys.
	if err := os.WriteFile(p, []byte(liveTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	const buildCmd = "darwin-rebuild build --flake {terminal_nix_dir}"
	const applyCmd = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"
	var out bytes.Buffer
	code := run([]string{
		"--root", dir,
		"--id", "phillipg-mbp",
		"--apply-post", "pb gate check",
		"--build-command", buildCmd,
		"--apply-command", applyCmd,
	}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0. output: %s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("enforced")) {
		t.Errorf("expected an 'enforced' line, got: %q", out.String())
	}
	data, _ := os.ReadFile(p)
	if !bytes.Contains(data, []byte("build_command = 'darwin-rebuild build --flake {terminal_nix_dir}'")) {
		t.Errorf("build_command not written verbatim: %s", data)
	}
	if !bytes.Contains(data, []byte("apply_command = 'sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}'")) {
		t.Errorf("apply_command not written verbatim: %s", data)
	}
	// terminal must be preserved (pn-owned).
	if !bytes.Contains(data, []byte("terminal = 'phillipg-nix-ziprecruiter'")) {
		t.Errorf("terminal not preserved: %s", data)
	}
	// repos preserved.
	if !bytes.Contains(data, []byte("[repos.phillipg-nix-repo-base]")) {
		t.Errorf("repos not preserved: %s", data)
	}
}

// Omitting --build-command / --apply-command leaves those keys untouched
// (key-scoped) even when the required keys are enforced.
func TestRun_OmittedCommandsLeaveKeysUntouched(t *testing.T) {
	dir := t.TempDir()
	withCustom := `[workspace]
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'
build_command = 'custom'
apply_command = 'custom apply'

[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:x/y.git'

[[hooks]]
when = ['post-apply', 'post-upgrade']
run = ['pb gate check']
`
	p := filepath.Join(dir, "pn-workspace.toml")
	if err := os.WriteFile(p, []byte(withCustom), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	var out bytes.Buffer
	// No --build-command / --apply-command flags; everything else already correct.
	code := run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0. output: %s", code, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("enforced")) {
		t.Errorf("printed 'enforced' on a no-op: %q", out.String())
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Errorf("file changed when command flags omitted:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// --activate-command enforces the optional key (bead pg2-sdbhh4.9), reports a
// change, and an omitted flag leaves the key untouched.
func TestRun_EnforcesActivateCommand(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pn-workspace.toml")
	if err := os.WriteFile(p, []byte(liveTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	const activateCmd = "sudo {out}/sw/bin/darwin-rebuild activate"
	var out bytes.Buffer
	code := run([]string{
		"--root", dir,
		"--id", "phillipg-mbp",
		"--apply-post", "pb gate check",
		"--activate-command", activateCmd,
	}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0. output: %s", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("enforced")) {
		t.Errorf("expected an 'enforced' line, got: %q", out.String())
	}
	data, _ := os.ReadFile(p)
	if !bytes.Contains(data, []byte("activate_command = 'sudo {out}/sw/bin/darwin-rebuild activate'")) {
		t.Errorf("activate_command not written verbatim:\n%s", data)
	}

	// A later run WITHOUT the flag neither removes nor changes it, and is silent.
	out.Reset()
	code = run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check"}, &out)
	if code != 0 || out.Len() != 0 {
		t.Errorf("run without --activate-command: code=%d out=%q, want a silent no-op", code, out.String())
	}
	data2, _ := os.ReadFile(p)
	if !bytes.Equal(data, data2) {
		t.Errorf("a run without the flag changed the file:\n%s", data2)
	}
}

// A value pn cannot parse is rejected with a non-zero exit and nothing written.
func TestRun_RejectsABadActivateCommand(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pn-workspace.toml")
	if err := os.WriteFile(p, []byte(liveTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--root", dir, "--id", "phillipg-mbp", "--apply-post", "pb gate check", "--activate-command", "sudo /usr/bin/true"}, &out)
	if code == 0 {
		t.Errorf("exit code = 0; want non-zero for an activate_command that does not start with {out}/")
	}
	data, _ := os.ReadFile(p)
	if bytes.Contains(data, []byte("activate_command")) {
		t.Errorf("a rejected value was written:\n%s", data)
	}
}
