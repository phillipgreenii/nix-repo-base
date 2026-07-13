// Package workspace implements the pn workspace commands.
package workspace

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// WorkspaceConfig is the parsed representation of pn-workspace.toml.
type WorkspaceConfig struct {
	Workspace WorkspaceSection      `toml:"workspace"`
	Repos     map[string]RepoConfig `toml:"repos"`
	// Hooks are workspace-scoped event hooks: each runs once at the workspace
	// root when its `when` event fires. See EventHook (bd pg2-5yq5).
	Hooks []EventHook `toml:"hooks,omitempty"`
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
	// Hooks are per-repo event hooks: each runs in THIS repo (cwd=repo) when its
	// `when` event fires for a command that processes the repo. A `{nix_run
	// <attr>}` token in `run` expands to an override-aware `nix run` against this
	// repo's flake (bd pg2-5yq5).
	Hooks []EventHook `toml:"hooks,omitempty"`
}

// EventHook is one event hook entry ([[hooks]] or [[repos.<r>.hooks]]). When
// lists the events (`<pre|post>-<command>`, e.g. "post-rebase") that fire it;
// Run lists the shell commands to execute. Used for both workspace-scoped and
// per-repo hooks (bd pg2-5yq5).
type EventHook struct {
	When []string `toml:"when"`
	Run  []string `toml:"run"`
}

var workspaceIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// repoNameRe constrains a [repos.<name>] key to a single safe path segment. The
// name becomes a directory under the workspace root (filepath.Join(root, name)),
// so a value with a path separator ("../elsewhere"), or a leading '.' ("..") or
// '-', could escape the root or be misread as a git option (bead pg2-3j8b2).
// Forbidding path separators also guarantees name == filepath.Base(name). Real
// repo names are letters/digits/dash/underscore/dot, e.g. "phillipg-nix-repo-base"
// or "a_dep".
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

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

// legacyHookCommand mirrors the pre-ADR-0019 [hooks.<command>] table shape
// (pre/post arrays), so ParseConfig can detect a config that predates the
// event-hook list schema and emit an actionable migration message instead of
// go-toml's opaque "cannot store a table in a slice" (bd pg2-lbsi).
type legacyHookCommand struct {
	Pre  []string `toml:"pre,omitempty"`
	Post []string `toml:"post,omitempty"`
}

// legacyHooksConfig parses `hooks` as the removed map-of-tables shape. It only
// unmarshals successfully against the OLD schema; the new [[hooks]]
// array-of-tables makes this unmarshal fail (array into map), so a migrated
// config is never flagged.
type legacyHooksConfig struct {
	Hooks map[string]legacyHookCommand `toml:"hooks"`
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

	// Detect the pre-ADR-0019 [hooks.<command>] table schema and guide migration
	// before go-toml's opaque "cannot store a table in a slice" surfaces. This
	// path also fires at home.activation via pn-workspace-toml-enforce, so the
	// message MUST be actionable during the coordinated cutover (bd pg2-lbsi).
	var legacyHooks legacyHooksConfig
	if err := toml.Unmarshal(data, &legacyHooks); err == nil && len(legacyHooks.Hooks) > 0 {
		cmds := make([]string, 0, len(legacyHooks.Hooks))
		for cmd := range legacyHooks.Hooks {
			cmds = append(cmds, cmd)
		}
		sort.Strings(cmds)
		return nil, fmt.Errorf(
			"pn-workspace.toml uses the removed [hooks.<command>] table schema (found: [hooks.%s]); "+
				"migrate each to an event-hook list: [hooks.apply] post=['pb gate check'] becomes "+
				"[[hooks]] when=['post-apply'] run=['pb gate check'] (a pre array maps to when=['pre-<command>']); "+
				"per-repo hooks move to [[repos.<key>.hooks]]. See ADR-0019",
			strings.Join(cmds, "], [hooks."),
		)
	}

	cfg := &WorkspaceConfig{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse pn-workspace.toml: %w", err)
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]RepoConfig)
	}
	// Validate workspace.id if set.
	if cfg.Workspace.Id != "" && !workspaceIDRe.MatchString(cfg.Workspace.Id) {
		return nil, fmt.Errorf("workspace.id %q must be a slug: lowercase letters, digits, dashes", cfg.Workspace.Id)
	}
	// Apply repo defaults + validate each repo.
	for name, r := range cfg.Repos {
		// Reject a name that is not a single safe path segment before it is ever
		// joined onto the workspace root as a directory (bead pg2-3j8b2).
		if !repoNameRe.MatchString(name) {
			return nil, fmt.Errorf(
				"repo name %q is invalid: must be a single path segment (letters, digits, '.', '_', '-'; no path separators; no leading '.' or '-')",
				name,
			)
		}
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
	// Event-hook validation: every `when` event is a known <pre|post>-<command>,
	// {nix_run} placement/count is legal, and no malformed {nix_run …} token
	// slips through as a literal (see validateAllHooks).
	if err := validateAllHooks(cfg); err != nil {
		return nil, err
	}
	if t := cfg.Workspace.Terminal; t != "" {
		if _, ok := cfg.Repos[t]; !ok {
			return nil, fmt.Errorf("workspace.terminal %q does not match any [repos.*] entry", t)
		}
	}
	return cfg, nil
}
