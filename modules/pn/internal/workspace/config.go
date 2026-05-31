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
}

// RepoConfig describes one entry under [repos.<name>].
type RepoConfig struct {
	URL    string `toml:"url"`
	Branch string `toml:"branch"`
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
		if r.URL == "" {
			return nil, fmt.Errorf("repo %q: url is required", name)
		}
		if r.Branch == "" {
			r.Branch = "main"
		}
		cfg.Repos[name] = r
	}
	// Validate hook command names.
	for cmd := range cfg.Hooks {
		if !IsKnownHookCommand(cmd) {
			return nil, fmt.Errorf("hooks.%s: unknown pn-workspace command", cmd)
		}
	}
	return cfg, nil
}
