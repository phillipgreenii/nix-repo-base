package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "")
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

	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check",
		"darwin-rebuild build --flake {terminal_flake}",
		"sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}")
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
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "")
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
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "")
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
	if _, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", ""); err != nil {
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
	if _, err := EnforceKeys(p, "Phillip_MBP", "pb gate check", "", ""); err == nil {
		t.Errorf("expected error for non-slug id, got nil")
	}
}

// The committed template strings (verbatim; {terminal_flake}/{hostname} are pn
// placeholders that must survive the round-trip literally). Shared by the
// build_command / apply_command tests below.
const (
	committedBuildCommand = "darwin-rebuild build --flake {terminal_flake}"
	committedApplyCommand = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
)

// Setting build_command + apply_command rewrites the file, updates ONLY those two
// keys, and preserves the [repos.*] entries, workspace.terminal, and everything
// else. This is the primary pg2-k43p.8 behavior.
func TestEnforceKeys_SetsBuildAndApplyCommandsPreservesReposAndTerminal(t *testing.T) {
	// Start from a file whose build/apply commands are wrong (empty) and whose
	// id/apply-post/terminal are already right.
	wrongCommands := `[workspace]
name = ''
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'

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
	p := writeTemp(t, "pn-workspace.toml", wrongCommands, 0o600)
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", committedBuildCommand, committedApplyCommand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false; want true when build/apply commands differ")
	}
	cfg, err := loadConfigFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.BuildCommand != committedBuildCommand {
		t.Errorf("build_command = %q; want %q", cfg.Workspace.BuildCommand, committedBuildCommand)
	}
	if cfg.Workspace.ApplyCommand != committedApplyCommand {
		t.Errorf("apply_command = %q; want %q", cfg.Workspace.ApplyCommand, committedApplyCommand)
	}
	// terminal must be preserved (pn-owned; NEVER touched by the enforcer).
	if cfg.Workspace.Terminal != "phillipg-nix-ziprecruiter" {
		t.Errorf("terminal = %q; want phillipg-nix-ziprecruiter (must be preserved)", cfg.Workspace.Terminal)
	}
	// repos preserved verbatim.
	if r, ok := cfg.Repos["phillipg-nix-repo-base"]; !ok {
		t.Errorf("repo phillipg-nix-repo-base dropped")
	} else if r.URL != "git@github.com:phillipgreenii/nix-repo-base.git" {
		t.Errorf("repo url mangled: %q", r.URL)
	}
	if _, ok := cfg.Repos["phillipg-nix-ziprecruiter"]; !ok {
		t.Errorf("repo phillipg-nix-ziprecruiter dropped")
	}
	// id + apply-post preserved.
	if cfg.Workspace.Id != "phillipg-mbp" {
		t.Errorf("id = %q; want phillipg-mbp", cfg.Workspace.Id)
	}
	if got := cfg.Hooks["apply"].Post; !reflect.DeepEqual(got, []string{"pb gate check"}) {
		t.Errorf("apply.post = %v; want [pb gate check]", got)
	}
}

// Key-scoped: an empty build_command / apply_command leaves those keys UNTOUCHED
// (so terminal and any future unmanaged key are never touched). Here the file
// already has commands that differ from the committed values; passing "" for both
// must NOT overwrite them — only id/apply-post are enforced.
func TestEnforceKeys_EmptyBuildApplyLeavesThoseKeysUntouched(t *testing.T) {
	customCommands := `[workspace]
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'
build_command = 'custom build cmd'
apply_command = 'custom apply cmd'

[repos]
[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:phillipgziprecruiter/phillipg_mbp.git'
branch = 'main'

[hooks.apply]
post = ['pb gate check']
`
	p := writeTemp(t, "pn-workspace.toml", customCommands, 0o600)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// id + apply-post already correct AND build/apply left empty → full no-op.
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("changed = true; want false — empty build/apply must not touch existing custom values")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file rewritten when build/apply were empty:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	cfg, err := loadConfigFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.BuildCommand != "custom build cmd" {
		t.Errorf("build_command = %q; want the untouched custom value", cfg.Workspace.BuildCommand)
	}
	if cfg.Workspace.ApplyCommand != "custom apply cmd" {
		t.Errorf("apply_command = %q; want the untouched custom value", cfg.Workspace.ApplyCommand)
	}
}

// Enforcing all four keys against a file that already matches all of them is an
// idempotent no-op (empty diff): changed=false, bytes + mtime unchanged.
func TestEnforceKeys_AllFourKeysNoOpWhenAlreadyCorrect(t *testing.T) {
	p := writeTemp(t, "pn-workspace.toml", realWorldTOML, 0o600)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, _ := os.Stat(p)

	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", committedBuildCommand, committedApplyCommand)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("changed = true; want false when all four keys already match")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file rewritten on four-key no-op:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	afterInfo, _ := os.Stat(p)
	if beforeInfo.ModTime() != afterInfo.ModTime() {
		t.Errorf("mtime changed on no-op: %v -> %v", beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

// The four-key write preserves the original file mode (0600).
func TestEnforceKeys_PreservesMode0600WithCommands(t *testing.T) {
	wrong := `[workspace]
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'

[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:x/y.git'

[hooks.apply]
post = ['pb gate check']
`
	p := writeTemp(t, "pn-workspace.toml", wrong, 0o600)
	if _, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", committedBuildCommand, committedApplyCommand); err != nil {
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

// Absent file → no-op even when build/apply commands are supplied.
func TestEnforceKeys_AbsentFileNoOpWithCommands(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pn-workspace.toml")
	changed, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", committedBuildCommand, committedApplyCommand)
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

// The template placeholders {terminal_flake} / {hostname} survive the round-trip
// verbatim (they are pn expansion tokens, not something the enforcer resolves).
func TestEnforceKeys_PreservesTemplatePlaceholdersVerbatim(t *testing.T) {
	wrong := `[workspace]
id = 'phillipg-mbp'
terminal = 'phillipg-nix-ziprecruiter'
build_command = 'stale'
apply_command = 'stale'

[repos.phillipg-nix-ziprecruiter]
url = 'git@github.com:x/y.git'

[hooks.apply]
post = ['pb gate check']
`
	p := writeTemp(t, "pn-workspace.toml", wrong, 0o600)
	if _, err := EnforceKeys(p, "phillipg-mbp", "pb gate check", committedBuildCommand, committedApplyCommand); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "{terminal_flake}") {
		t.Errorf("{terminal_flake} placeholder not preserved verbatim in:\n%s", raw)
	}
	if !strings.Contains(string(raw), "{hostname}") {
		t.Errorf("{hostname} placeholder not preserved verbatim in:\n%s", raw)
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
