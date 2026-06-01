// Package workspace implements the pn workspace commands.
package workspace

import (
	"fmt"

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
	// Terminal is the repo key of the terminal flake — the one build/apply
	// build and activate; all others are injected as local overrides.
	Terminal string `toml:"terminal,omitempty"`
	// BuildCommand / ApplyCommand are command templates expanded with
	// {terminal_flake} and {hostname}. BuildCommand defaults when empty;
	// ApplyCommand is required by `apply`.
	BuildCommand string `toml:"build_command,omitempty"`
	ApplyCommand string `toml:"apply_command,omitempty"`
}

// Remote is one named git remote that publishes a workspace repo.
type Remote struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// RepoConfig describes one entry under [repos.<name>].
//
// Two complementary mechanisms for resolving a repo's upstream flake input:
//  1. Explicit override via InputName (simpler, opt-in per repo).
//  2. Topology-graph derivation via Remotes + Slug (auto-discovery; see
//     graph.go + checkRemoteAgreement, added by later commits).
//
// Default (both unset): the repo's key (directory name) is used as InputName.
type RepoConfig struct {
	URL       string   `toml:"url"`
	Branch    string   `toml:"branch"`
	InputName string   `toml:"input-name,omitempty"`
	Remotes   []Remote `toml:"remotes,omitempty"`
	Slug      string   `toml:"slug,omitempty"`
}

// HookCommand describes one entry under [hooks.<command>]; Pre/Post are
// ordered lists of shell command strings.
type HookCommand struct {
	Pre  []string `toml:"pre"`
	Post []string `toml:"post"`
}

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

const defaultBuildCommand = "darwin-rebuild build --flake {terminal_flake}"

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

// InputNameFor returns the flake input name to override for the workspace repo
// keyed by repoKey: the repo's explicit input-name if set, otherwise repoKey
// itself (the on-disk directory name). Unknown repos fall back to repoKey.
// Nil-safe so override computation can call it unconditionally.
func (c *WorkspaceConfig) InputNameFor(repoKey string) string {
	if c != nil {
		if r, ok := c.Repos[repoKey]; ok && r.InputName != "" {
			return r.InputName
		}
	}
	return repoKey
}

// ParseConfig parses pn-workspace.toml bytes into a WorkspaceConfig. Applies
// defaults (e.g., empty branch → "main") and validates the shape.
func ParseConfig(data []byte) (*WorkspaceConfig, error) {
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
