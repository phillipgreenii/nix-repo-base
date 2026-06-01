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

func TestParseConfig_RepoInputName(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
[repos.phillipg-nix-repo-base]
url = "github:phillipgreenii/nix-repo-base"
input-name = "phillipgreenii-nix-base"

[repos.nix-overlay]
url = "github:phillipgreenii/nix-overlay"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Explicit input-name is honored.
	if got := cfg.InputNameFor("phillipg-nix-repo-base"); got != "phillipgreenii-nix-base" {
		t.Errorf("explicit input-name: got %q want phillipgreenii-nix-base", got)
	}
	// Omitted input-name defaults to the repo key (the on-disk directory name).
	if got := cfg.InputNameFor("nix-overlay"); got != "nix-overlay" {
		t.Errorf("default input-name: got %q want nix-overlay", got)
	}
	// Unknown repo falls back to the key itself.
	if got := cfg.InputNameFor("does-not-exist"); got != "does-not-exist" {
		t.Errorf("unknown repo: got %q want does-not-exist", got)
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
build_command = "darwin-rebuild build --flake {terminal_flake}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"

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
	if got := cfg.BuildCommandTemplate(); got != "darwin-rebuild build --flake {terminal_flake}" {
		t.Errorf("BuildCommandTemplate: got %q", got)
	}
	ac, err := cfg.ApplyCommandTemplate()
	if err != nil || ac != "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}" {
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
	if got := cfg.BuildCommandTemplate(); got != "darwin-rebuild build --flake {terminal_flake}" {
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
