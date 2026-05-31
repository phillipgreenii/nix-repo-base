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

func TestParseConfig_RejectsMissingRepoURL(t *testing.T) {
	bad := `[repos.foo]
branch = "main"
`
	_, err := ParseConfig([]byte(bad))
	if err == nil {
		t.Fatal("expected error for missing url; got nil")
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

func TestKnownHookCommands(t *testing.T) {
	want := []string{"apply", "build", "flake-check", "init", "pre-commit-check", "push", "rebase", "status", "tree", "update", "upgrade"}
	for _, c := range want {
		if !IsKnownHookCommand(c) {
			t.Errorf("expected %q to be a known hook command", c)
		}
	}
	if IsKnownHookCommand("not-a-real-command") {
		t.Error("not-a-real-command should not be known")
	}
}
