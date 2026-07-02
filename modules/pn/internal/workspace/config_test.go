package workspace

import (
	"strings"
	"testing"
)

const sampleTOML = `
[workspace]
name = "phillipgreenii"
description = "phillipgreenii's nix workspace"

[repos.nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
branch = "main"

[repos.nix-overlay]
url = "github:phillipgreenii/nix-overlay"

[hooks.update]
pre = ["pn-osx-tcc-check", "./hooks/check-vault-ready.sh"]
post = ["echo update done"]

[hooks.build]
pre = ["./hooks/preflight.sh"]
`

func TestParseConfig_Workspace(t *testing.T) {
	cfg, err := ParseConfig([]byte(sampleTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Workspace.Name != "phillipgreenii" {
		t.Errorf("name: got %q want phillipgreenii", cfg.Workspace.Name)
	}
}

func TestParseConfig_Repos(t *testing.T) {
	cfg, err := ParseConfig([]byte(sampleTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}
	r, ok := cfg.Repos["nix-repo-base"]
	if !ok {
		t.Fatal("missing repo nix-repo-base")
	}
	if r.URL != "github:phillipgreenii/nix-repo-base" {
		t.Errorf("url: got %q", r.URL)
	}
	if r.Branch != "main" {
		t.Errorf("branch: got %q want main", r.Branch)
	}
}

func TestParseConfig_RepoDefaultBranch(t *testing.T) {
	cfg, err := ParseConfig([]byte(sampleTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := cfg.Repos["nix-overlay"]
	if r.Branch != "main" {
		t.Errorf("expected default branch main, got %q", r.Branch)
	}
}

func TestParseConfig_Hooks(t *testing.T) {
	cfg, err := ParseConfig([]byte(sampleTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	update := cfg.Hooks["update"]
	if len(update.Pre) != 2 {
		t.Errorf("update.pre: got %d entries", len(update.Pre))
	}
	if update.Pre[0] != "pn-osx-tcc-check" {
		t.Errorf("update.pre[0]: got %q", update.Pre[0])
	}
	if len(update.Post) != 1 {
		t.Errorf("update.post: got %d entries", len(update.Post))
	}
}

func TestParseConfig_RejectsUnknownHookCommand(t *testing.T) {
	bad := `[hooks.notacommand]
pre = ["foo"]
`
	_, err := ParseConfig([]byte(bad))
	if err == nil {
		t.Fatal("expected error for unknown hook command; got nil")
	}
	if !strings.Contains(err.Error(), "notacommand") {
		t.Errorf("error should name the bad command; got %q", err.Error())
	}
}

func TestParseConfig_RejectsUnknownPlaceholder(t *testing.T) {
	// A stale {terminal_flake} (or any typo) in build_command/apply_command must
	// fail at config-load, not silently at build/apply time.
	badBuild := `[workspace]
build_command = "nixos-rebuild build --flake {terminal_flake}"
`
	if _, err := ParseConfig([]byte(badBuild)); err == nil {
		t.Fatal("expected error for unknown build_command placeholder; got nil")
	} else if !strings.Contains(err.Error(), "terminal_flake") {
		t.Errorf("error should name the bad placeholder; got %q", err.Error())
	}

	badApply := `[workspace]
apply_command = "sudo {builder} switch --flake {bogus}"
`
	if _, err := ParseConfig([]byte(badApply)); err == nil {
		t.Fatal("expected error for unknown apply_command placeholder; got nil")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bad placeholder; got %q", err.Error())
	}

	// Known placeholders — plus a legitimate lowercase ${shellvar} the $-aware
	// scan must not mistake for a placeholder — parse cleanly.
	good := `[workspace]
apply_command = "sudo {builder} switch --flake {terminal_nix_dir}#{hostname}"
build_command = "sh -c 'echo ${home}; {builder} build --flake {terminal_repo_dir}/{terminal_nix_relative_path}'"
`
	if _, err := ParseConfig([]byte(good)); err != nil {
		t.Fatalf("valid placeholders should parse cleanly, got %v", err)
	}
}

func TestParseConfig_RejectsMissingURLAndRemotes(t *testing.T) {
	bad := `[repos.foo]
branch = "main"
`
	_, err := ParseConfig([]byte(bad))
	if err == nil {
		t.Fatal("expected error: neither url nor remotes provided")
	}
}

func TestParseConfig_EmptyConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(""))
	if err != nil {
		t.Fatalf("empty config should parse cleanly, got %v", err)
	}
	if cfg.Repos == nil {
		t.Error("expected Repos map (possibly empty) to be non-nil")
	}
}

// TestParseConfig_RejectsInputName verifies that ParseConfig returns a clear
// migration error when any [repos.*] entry still has the removed input-name
// field, with guidance to remove it and rely on per-edge lock aliases instead.
func TestParseConfig_RejectsInputName(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.phillipg-nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
input-name = "phillipgreenii-nix-base"

[repos.nix-overlay]
url = "github:phillipgreenii/nix-overlay"
`))
	if err == nil {
		t.Fatal("expected error for legacy input-name field; got nil")
	}
	if !strings.Contains(err.Error(), "input-name") {
		t.Errorf("error should mention input-name; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "phillipg-nix-repo-base") {
		t.Errorf("error should name the offending repo; got %q", err.Error())
	}
}

func TestKnownHookCommands(t *testing.T) {
	want := []string{"apply", "build", "flake-check", "init", "lock", "pre-commit-check", "push", "rebase", "status", "tree", "update", "upgrade"}
	for _, c := range want {
		if !IsKnownHookCommand(c) {
			t.Errorf("expected %q to be a known hook command", c)
		}
	}
	if IsKnownHookCommand("not-a-real-command") {
		t.Error("not-a-real-command should not be known")
	}
}

func TestParseConfig_WorkspaceCommands(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[workspace]
terminal = "leaf"
build_command = "darwin-rebuild build --flake {terminal_nix_dir}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}"

[repos.leaf]
url = "github:owner/leaf"

[repos.dep]
url = "github:owner/dep"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	term, err := cfg.TerminalRepo()
	if err != nil || term != "leaf" {
		t.Fatalf("TerminalRepo: got %q, %v", term, err)
	}
	if got := cfg.BuildCommandTemplate(); got != "darwin-rebuild build --flake {terminal_nix_dir}" {
		t.Errorf("BuildCommandTemplate: got %q", got)
	}
	ac, err := cfg.ApplyCommandTemplate()
	if err != nil || ac != "sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}" {
		t.Errorf("ApplyCommandTemplate: got %q, %v", ac, err)
	}
}

func TestParseConfig_TerminalMustNameRepo(t *testing.T) {
	_, err := ParseConfig([]byte(`
[workspace]
terminal = "nope"

[repos.leaf]
url = "github:owner/leaf"
`))
	if err == nil {
		t.Fatal("expected error for terminal not matching a repo")
	}
}

func TestParseConfig_DefaultsWhenCommandsAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte(`[repos.leaf]
url = "github:owner/leaf"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.BuildCommandTemplate(); got != "{builder} build --flake {terminal_nix_dir}" {
		t.Errorf("default build command: got %q", got)
	}
	if _, err := cfg.TerminalRepo(); err == nil {
		t.Error("expected error when terminal unset")
	}
	if _, err := cfg.ApplyCommandTemplate(); err == nil {
		t.Error("expected error when apply_command unset")
	}
}

func TestParseConfig_RejectsBothUrlAndRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
url = "github:o/foo"
remotes = [{ name = "origin", url = "github:o/foo" }]
`))
	if err == nil {
		t.Fatal("expected error: url + remotes are mutually exclusive")
	}
	if !strings.Contains(err.Error(), "foo") || !strings.Contains(err.Error(), "remotes") {
		t.Errorf("error should name the repo and remotes: %v", err)
	}
}

func TestParseConfig_RejectsEmptyRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = []
`))
	if err == nil {
		t.Fatal("expected error: empty remotes is invalid")
	}
}

func TestParseConfig_RejectsMultipleOriginRemotes(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = [
  { name = "origin", url = "github:o/foo" },
  { name = "origin", url = "github:o/bar" },
]
`))
	if err == nil {
		t.Fatal("expected error: at most one remote may be named origin")
	}
}

func TestParseConfig_AcceptsRemotesWithoutOrigin(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
remotes = [
  { name = "bitbucket", url = "git@bitbucket.org:o/foo.git" },
  { name = "gitlab",    url = "git@gitlab.com:o/foo.git" },
]
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Repos["foo"].Remotes) != 2 {
		t.Errorf("expected 2 remotes, got %d", len(cfg.Repos["foo"].Remotes))
	}
}

func TestParseConfig_AcceptsExplicitSlug(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
url = "github:o/foo"
slug = "o/canonical"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Repos["foo"].Slug != "o/canonical" {
		t.Errorf("Slug: got %q", cfg.Repos["foo"].Slug)
	}
}

func TestParseConfig_AcceptsWorkspaceTerminal(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[workspace]
name = "x"
terminal = "foo"

[repos.foo]
url = "github:o/foo"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workspace.Terminal != "foo" {
		t.Errorf("Terminal: got %q", cfg.Workspace.Terminal)
	}
}

func TestParseConfig_RejectsTerminalPointingAtUnknownRepo(t *testing.T) {
	_, err := ParseConfig([]byte(`
[workspace]
name = "x"
terminal = "nonexistent"

[repos.foo]
url = "github:o/foo"
`))
	if err == nil {
		t.Fatal("expected error: terminal names a repo not in [repos.*]")
	}
}

func TestParseConfig_RejectsRemoteWithMissingName(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = [{ url = "github:o/foo" }]
`))
	if err == nil {
		t.Fatal("expected error: remote entry missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention missing name: %v", err)
	}
}

func TestParseConfig_RejectsRemoteWithMissingURL(t *testing.T) {
	_, err := ParseConfig([]byte(`
[repos.foo]
remotes = [{ name = "origin" }]
`))
	if err == nil {
		t.Fatal("expected error: remote missing url")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention missing url: %v", err)
	}
}

func TestParseConfig_AcceptsSlugWithRemotes(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
slug = "o/canonical"
remotes = [
  { name = "origin", url = "github:o/foo" },
  { name = "mirror", url = "github:o/foo-mirror" },
]
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Repos["foo"].Slug != "o/canonical" {
		t.Errorf("Slug: got %q", cfg.Repos["foo"].Slug)
	}
	if len(cfg.Repos["foo"].Remotes) != 2 {
		t.Errorf("Remotes: got %d", len(cfg.Repos["foo"].Remotes))
	}
}

// TestParseConfig_WorkforestsDirField verifies that workforests_dir in [workspace]
// is parsed into WorkspaceSection.WorkforestsDir.
func TestParseConfig_WorkforestsDirField(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[workspace]
workforests_dir = "sets"

[repos.foo]
url = "github:o/foo"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workspace.WorkforestsDir != "sets" {
		t.Errorf("WorkforestsDir: got %q, want %q", cfg.Workspace.WorkforestsDir, "sets")
	}
}

// TestParseConfig_WorkforestsDirAbsent verifies that when workforests_dir is absent,
// WorkforestsDir is empty and WorkforestsDirName returns the default ".workforests".
func TestParseConfig_WorkforestsDirAbsent(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.foo]
url = "github:o/foo"
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workspace.WorkforestsDir != "" {
		t.Errorf("WorkforestsDir: got %q, want empty", cfg.Workspace.WorkforestsDir)
	}
	if got := cfg.WorkforestsDirName(); got != ".workforests" {
		t.Errorf("WorkforestsDirName (absent): got %q, want .workforests", got)
	}
}

// TestWorkspaceConfig_WorkforestsDirName verifies WorkforestsDirName returns the
// configured value when set, and the default ".workforests" when empty.
func TestWorkspaceConfig_WorkforestsDirName(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{"empty returns default", "", ".workforests"},
		{"custom value returned", "sets", "sets"},
		{"dot prefix preserved", ".wt", ".wt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &WorkspaceConfig{
				Workspace: WorkspaceSection{WorkforestsDir: tc.configured},
			}
			if got := cfg.WorkforestsDirName(); got != tc.want {
				t.Errorf("WorkforestsDirName: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorkspace_WorkforestsDir verifies the Workspace.WorkforestsDir() accessor
// resolves relative paths under root and leaves absolute paths unchanged.
func TestWorkspace_WorkforestsDir(t *testing.T) {
	root := "/some/workspace"

	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{"default (.workforests)", "", "/some/workspace/.workforests"},
		{"relative name", "sets", "/some/workspace/sets"},
		{"absolute path unchanged", "/abs/wt", "/abs/wt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &Workspace{
				root: root,
				config: &WorkspaceConfig{
					Workspace: WorkspaceSection{WorkforestsDir: tc.configured},
				},
			}
			if got := w.WorkforestsDir(); got != tc.want {
				t.Errorf("WorkforestsDir: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseConfig_WorkspaceID(t *testing.T) {
	cfg, err := ParseConfig([]byte("[workspace]\nid = \"my-ws-01\"\nterminal = \"r\"\n[repos.r]\nurl=\"u\"\n"))
	if err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
	if cfg.Workspace.Id != "my-ws-01" {
		t.Fatalf("id = %q, want my-ws-01", cfg.Workspace.Id)
	}
	if _, err := ParseConfig([]byte("[workspace]\nid = \"Bad_ID\"\nterminal=\"r\"\n[repos.r]\nurl=\"u\"\n")); err == nil {
		t.Fatal("malformed id (uppercase/underscore) should be rejected")
	}
}
