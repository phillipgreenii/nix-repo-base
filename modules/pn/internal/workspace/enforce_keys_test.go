package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// realWorldTOML mirrors the shape of the live pn-workspace.toml: a [workspace]
// section, several [repos.*] entries, and a [hooks.apply] table. Used to prove
// EnforceKeys touches ONLY workspace.id + hooks.apply.post and preserves the
// rest verbatim.
const realWorldTOML = `[workspace]
name = ''
description = ''
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'
build_command = 'darwin-rebuild build --flake {terminal_flake}'
apply_command = 'sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}'

[repos]
[repos.phillipg-nix-repo-base]
url = 'git@github.com:phillipgreenii/nix-repo-base.git'
branch = 'main'

[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:phillipgziprecruiter/phillipg_mbp.git'
branch = 'main'

[hooks.apply]
post = ['pb gate check']
`

func writeTemp(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

// When the file is absent, EnforceKeys is a no-op (does not create the file).
func TestEnforceKeys_AbsentFileNoOp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pn-workspace.toml")
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("changed = true; want false for an absent file")
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("EnforceKeys created a file for an absent path; want no-op")
	}
}

// When both keys already match, EnforceKeys is a no-op: it reports changed=false
// and does NOT rewrite the file (mtime + bytes unchanged).
func TestEnforceKeys_NoOpWhenAlreadyCorrect(t *testing.T) {
	p := writeTemp(t, "pn-workspace.toml", realWorldTOML, 0o600)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, _ := os.Stat(p)

	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("changed = true; want false when values already match")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file rewritten on no-op:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	afterInfo, _ := os.Stat(p)
	if beforeInfo.ModTime() != afterInfo.ModTime() {
		t.Errorf("mtime changed on no-op: %v -> %v", beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

// Setting a different id rewrites the file, updates only workspace.id, and
// preserves the [repos.*] entries and everything else.
func TestEnforceKeys_SetsIdPreservesRepos(t *testing.T) {
	// Start from a file whose id is wrong (empty) and whose apply.post is right.
	wrongID := `[workspace]
name = ''
id = ''

[repos.phillipg-nix-repo-base]
url = 'git@github.com:phillipgreenii/nix-repo-base.git'
branch = 'main'

[hooks.apply]
post = ['pb gate check']
`
	p := writeTemp(t, "pn-workspace.toml", wrongID, 0o600)
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false; want true when id differs")
	}
	cfg, err := loadConfigFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.Id != "phillipg-mbp" {
		t.Errorf("id = %q; want phillipg-mbp", cfg.Workspace.Id)
	}
	r, ok := cfg.Repos["phillipg-nix-repo-base"]
	if !ok {
		t.Fatalf("repo phillipg-nix-repo-base dropped after enforce")
	}
	if r.URL != "git@github.com:phillipgreenii/nix-repo-base.git" {
		t.Errorf("repo url mangled: %q", r.URL)
	}
	if got := cfg.Hooks["apply"].Post; !reflect.DeepEqual(got, []string{"pb gate check"}) {
		t.Errorf("apply.post = %v; want [pb gate check]", got)
	}
}

// When [hooks] is entirely absent, EnforceKeys creates hooks.apply.post
// (create-if-missing) without disturbing repos.
func TestEnforceKeys_CreatesMissingHooksTable(t *testing.T) {
	noHooks := `[workspace]
id = 'phillipg-mbp'

[repos.phillipg-nix-repo-base]
url = 'git@github.com:phillipgreenii/nix-repo-base.git'
branch = 'main'
`
	p := writeTemp(t, "pn-workspace.toml", noHooks, 0o600)
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false; want true when hooks.apply.post must be created")
	}
	cfg, err := loadConfigFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hooks["apply"].Post; !reflect.DeepEqual(got, []string{"pb gate check"}) {
		t.Errorf("apply.post = %v; want [pb gate check]", got)
	}
	if _, ok := cfg.Repos["phillipg-nix-repo-base"]; !ok {
		t.Errorf("repo dropped after creating hooks table")
	}
}

// The write preserves the original file mode (0600).
func TestEnforceKeys_PreservesMode0600(t *testing.T) {
	wrongID := `[workspace]
id = ''

[repos.r]
url = 'git@github.com:x/y.git'
`
	p := writeTemp(t, "pn-workspace.toml", wrongID, 0o600)
	if _, err := EnforceKeys(p, "phillipg-mbp", "pb gate check"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o; want 600", perm)
	}
}

// A rejected id (not a slug) is a build-time concern in nix; but EnforceKeys must
// also reject it defensively rather than write garbage.
func TestEnforceKeys_RejectsBadID(t *testing.T) {
	p := writeTemp(t, "pn-workspace.toml", realWorldTOML, 0o600)
	if _, err := EnforceKeys(p, "Phillip_MBP", "pb gate check"); err == nil {
		t.Errorf("expected error for non-slug id, got nil")
	}
}

// loadConfigFile is a test helper: read + ParseConfig a file.
func loadConfigFile(t *testing.T, path string) (*WorkspaceConfig, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}
