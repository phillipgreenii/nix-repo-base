// Package workspace implements the pn workspace commands.
package workspace

import (
	"fmt"
	"regexp"

	"github.com/pelletier/go-toml/v2"
)

// WorkspaceConfig is the parsed representation of pn-workspace.toml.
type WorkspaceConfig struct {
	Workspace WorkspaceSection       `toml:"workspace"`
	Repos     map[string]RepoConfig  `toml:"repos"`
	Hooks     map[string]HookCommand `toml:"hooks"`
}

// WorkspaceSection is the [workspace] table.
type WorkspaceSection struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	// Id is a stable, committed, human-readable workspace identifier (slug).
	// It is the wsid used by pn:applied gates; machine-invariant.
	Id string `toml:"id,omitempty"`
	// Terminal is the repo key of the terminal flake — the one build/apply
	// build and activate; all others are injected as local overrides.
	Terminal string `toml:"terminal,omitempty"`
	// BuildCommand / ApplyCommand are command templates expanded with the
	// placeholders {terminal_repo_dir}, {terminal_nix_dir},
	// {terminal_nix_relative_path}, {hostname}, and {builder} (the OS-detected
	// activation tool). BuildCommand defaults when empty; ApplyCommand is
	// required by `apply`. See ADR 0017.
	BuildCommand string `toml:"build_command,omitempty"`
	ApplyCommand string `toml:"apply_command,omitempty"`
	// WorkforestsDir is where `pn workspace workforest` creates sets. Relative paths are
	// resolved against the workspace root. Defaults to ".workforests" when empty.
	WorkforestsDir string `toml:"workforests_dir,omitempty"`
}

// Remote is one named git remote that publishes a workspace repo.
type Remote struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// RepoConfig describes one entry under [repos.<name>].
//
// Per-edge dependency aliases are derived at lock time from flake input URLs
// (see edges.go + LockEdge.Alias); they are NOT stored in pn-workspace.toml.
type RepoConfig struct {
	URL     string   `toml:"url"`
	Branch  string   `toml:"branch"`
	Remotes []Remote `toml:"remotes,omitempty"`
	Slug    string   `toml:"slug,omitempty"`
	// FlakePath is the path to the repo's flake.nix relative to the repo root.
	// When set, this overrides the default search paths (flake.nix, nix/flake.nix).
	// Recorded in pn-workspace.toml only for non-default locations.
	FlakePath string `toml:"flake_path,omitempty"`
}

// HookCommand describes one entry under [hooks.<command>]; Pre/Post are
// ordered lists of shell command strings.
type HookCommand struct {
	Pre  []string `toml:"pre"`
	Post []string `toml:"post"`
}

var workspaceIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// knownHookCommands is the set of pn-workspace commands that support hooks.
var knownHookCommands = map[string]struct{}{
	"apply":            {},
	"build":            {},
	"flake-check":      {},
	"init":             {},
	"lock":             {},
	"pre-commit-check": {},
	"push":             {},
	"rebase":           {},
	"status":           {},
	"tree":             {},
	"update":           {},
	"upgrade":          {},
}

// IsKnownHookCommand reports whether name is a recognized pn-workspace command.
func IsKnownHookCommand(name string) bool {
	_, ok := knownHookCommands[name]
	return ok
}

const defaultBuildCommand = "{builder} build --flake {terminal_nix_dir}"

// TerminalRepo returns the configured terminal repo key, or an error if unset.
func (c *WorkspaceConfig) TerminalRepo() (string, error) {
	if c == nil || c.Workspace.Terminal == "" {
		return "", fmt.Errorf("workspace.terminal is not set in pn-workspace.toml")
	}
	return c.Workspace.Terminal, nil
}

// BuildCommandTemplate returns the configured build_command, or the default.
func (c *WorkspaceConfig) BuildCommandTemplate() string {
	if c != nil && c.Workspace.BuildCommand != "" {
		return c.Workspace.BuildCommand
	}
	return defaultBuildCommand
}

// ApplyCommandTemplate returns the configured apply_command, or an error if unset.
func (c *WorkspaceConfig) ApplyCommandTemplate() (string, error) {
	if c == nil || c.Workspace.ApplyCommand == "" {
		return "", fmt.Errorf("workspace.apply_command is not set in pn-workspace.toml")
	}
	return c.Workspace.ApplyCommand, nil
}

const defaultWorkforestsDir = ".workforests"

// WorkforestsDirName returns the raw configured workforests_dir value, or the
// default ".workforests" when the field is empty. For a resolved absolute path,
// use Workspace.WorkforestsDir().
func (c *WorkspaceConfig) WorkforestsDirName() string {
	if c != nil && c.Workspace.WorkforestsDir != "" {
		return c.Workspace.WorkforestsDir
	}
	return defaultWorkforestsDir
}

// legacyInputName is a sentinel struct for detecting the removed input-name field.
// We parse into a parallel map to detect its presence and emit a migration error.
type legacyRepoConfig struct {
	InputName string `toml:"input-name,omitempty"`
}

type legacyWorkspaceConfig struct {
	Repos map[string]legacyRepoConfig `toml:"repos"`
}

// ParseConfig parses pn-workspace.toml bytes into a WorkspaceConfig. Applies
// defaults (e.g., empty branch → "main") and validates the shape. Returns an
// error if any [repos.*] entry still has the removed input-name field.
func ParseConfig(data []byte) (*WorkspaceConfig, error) {
	// First pass: detect legacy input-name fields before unmarshalling into the
	// current schema (which no longer has that field and would silently drop it).
	var legacy legacyWorkspaceConfig
	if err := toml.Unmarshal(data, &legacy); err == nil {
		for name, lr := range legacy.Repos {
			if lr.InputName != "" {
				return nil, fmt.Errorf(
					"repo %q: input-name is no longer supported; aliases are derived per-edge from flake input URLs at lock time; remove this field from pn-workspace.toml",
					name,
				)
			}
		}
	}

	cfg := &WorkspaceConfig{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse pn-workspace.toml: %w", err)
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]RepoConfig)
	}
	if cfg.Hooks == nil {
		cfg.Hooks = make(map[string]HookCommand)
	}
	// Validate workspace.id if set.
	if cfg.Workspace.Id != "" && !workspaceIDRe.MatchString(cfg.Workspace.Id) {
		return nil, fmt.Errorf("workspace.id %q must be a slug: lowercase letters, digits, dashes", cfg.Workspace.Id)
	}
	// Apply repo defaults + validate each repo.
	for name, r := range cfg.Repos {
		if r.URL != "" && len(r.Remotes) > 0 {
			return nil, fmt.Errorf("repo %q: url and remotes are mutually exclusive", name)
		}
		if r.URL == "" && len(r.Remotes) == 0 {
			return nil, fmt.Errorf("repo %q: must specify url or remotes", name)
		}
		if len(r.Remotes) > 0 {
			originCount := 0
			for _, rm := range r.Remotes {
				if rm.Name == "origin" {
					originCount++
				}
				if rm.Name == "" {
					return nil, fmt.Errorf("repo %q: remote entry missing name", name)
				}
				if rm.URL == "" {
					return nil, fmt.Errorf("repo %q: remote %q missing url", name, rm.Name)
				}
			}
			if originCount > 1 {
				return nil, fmt.Errorf("repo %q: at most one remote may be named origin (found %d)", name, originCount)
			}
		}
		if r.Branch == "" {
			r.Branch = "main"
		}
		cfg.Repos[name] = r
	}
	// Validate workspace.terminal points at a declared repo.
	if cfg.Workspace.Terminal != "" {
		if _, ok := cfg.Repos[cfg.Workspace.Terminal]; !ok {
			return nil, fmt.Errorf("workspace.terminal %q is not a declared repo", cfg.Workspace.Terminal)
		}
	}
	// Validate build_command / apply_command placeholders at parse time so a
	// stale {terminal_flake} (or any typo) fails at config-load instead of only
	// at build/apply. Static NAME check only — {builder} emptiness is
	// host-dependent and stays a run-time guard (see substituteCommand).
	if bc := cfg.Workspace.BuildCommand; bc != "" {
		if err := validateCommandPlaceholders("workspace.build_command", bc); err != nil {
			return nil, err
		}
	}
	if ac := cfg.Workspace.ApplyCommand; ac != "" {
		if err := validateCommandPlaceholders("workspace.apply_command", ac); err != nil {
			return nil, err
		}
	}
	// Validate hook command names.
	for cmd := range cfg.Hooks {
		if !IsKnownHookCommand(cmd) {
			return nil, fmt.Errorf("hooks.%s: unknown pn-workspace command", cmd)
		}
	}
	if t := cfg.Workspace.Terminal; t != "" {
		if _, ok := cfg.Repos[t]; !ok {
			return nil, fmt.Errorf("workspace.terminal %q does not match any [repos.*] entry", t)
		}
	}
	return cfg, nil
}
