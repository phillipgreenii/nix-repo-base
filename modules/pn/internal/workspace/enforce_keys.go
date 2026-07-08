package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// EnforceKeys reconciles the nix-owned keys in a pn-workspace.toml against
// committed source values: [workspace].id, [hooks.apply].post, and — added by
// bead pg2-k43p.8 — the static command templates [workspace].build_command and
// [workspace].apply_command. It has create-if-missing / enforce-when-present
// semantics and is deliberately narrow: it NEVER renders or owns [repos.*],
// workspace.terminal, workspace.name/description, or any other key — pn owns
// those. In particular workspace.terminal is left pn-owned because pn validates
// it against [repos.*] (it is coupled to repo topology, which pn manages via
// init/doctor); see phillipg-nix-repo-base ADR 0017.
//
// Key-scoped enforcement: buildCommand and applyCommand are enforced ONLY when a
// non-empty value is supplied. An empty string leaves that key untouched, so the
// caller can enforce a subset (and any key not passed a value — including
// terminal and any future key — is never touched). id and applyPost are always
// required and always enforced.
//
// Semantics (all mandated by the pg2-k43p.6/.8 design guardrails):
//   - Absent file → no-op, returns (false, nil). (pn workspace init creates the
//     file; the next apply enforces.)
//   - Loads the file via ParseConfig, sets ONLY the enforced keys, and preserves
//     everything else verbatim by re-marshalling through the SAME orderedConfig
//     struct pn's own writer uses — so output is byte-identical to `pn init` /
//     `doctor --fix`. Template placeholders like {terminal_flake} / {hostname}
//     in the command strings are pn expansion tokens and are written verbatim.
//   - Writes only when a value actually differs (idempotent no-op otherwise);
//     returns (true, nil) iff it wrote.
//   - The write is atomic (tempfile + rename in the same dir) and preserves the
//     original file's mode (e.g. 0600).
//   - Rejects an id that is not a slug (^[a-z0-9][a-z0-9-]*$) rather than write
//     an invalid value; the nix layer also asserts this at eval time.
//
// It reuses pn's go-toml/v2 serialization rather than reimplementing TOML
// writing, so nix-driven activation and pn's own commands cannot fight.
func EnforceKeys(path, id, applyPost, buildCommand, applyCommand string) (bool, error) {
	if !workspaceIDRe.MatchString(id) {
		return false, fmt.Errorf("workspace.id %q must be a slug: lowercase letters, digits, dashes", id)
	}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		// Create-if-missing does NOT apply to an absent config file: pn owns
		// creation (pn workspace init). No-op until then.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return false, err
	}

	changed := false

	if cfg.Workspace.Id != id {
		cfg.Workspace.Id = id
		changed = true
	}

	// Ensure a post-apply workspace hook runs applyPost. Ensure-present
	// semantics (ADR 0017): if some [[hooks]] entry with a "post-apply" event
	// already contains applyPost, no-op; else append it to the first post-apply
	// entry, or create a dedicated entry when none exists. Idempotent, and never
	// clobbers other run commands the user added to a post-apply hook.
	found := false
	for i := range cfg.Hooks {
		if slices.Contains(cfg.Hooks[i].When, "post-apply") {
			if !slices.Contains(cfg.Hooks[i].Run, applyPost) {
				cfg.Hooks[i].Run = append(cfg.Hooks[i].Run, applyPost)
				changed = true
			}
			found = true
			break
		}
	}
	if !found {
		cfg.Hooks = append(cfg.Hooks, EventHook{When: []string{"post-apply"}, Run: []string{applyPost}})
		changed = true
	}

	// Key-scoped: enforce build_command / apply_command ONLY when a non-empty
	// value is supplied. An empty value leaves the existing key untouched (so a
	// caller enforcing only id+applyPost never disturbs these, and terminal /
	// any future key is likewise never touched).
	if buildCommand != "" && cfg.Workspace.BuildCommand != buildCommand {
		cfg.Workspace.BuildCommand = buildCommand
		changed = true
	}
	if applyCommand != "" && cfg.Workspace.ApplyCommand != applyCommand {
		cfg.Workspace.ApplyCommand = applyCommand
		changed = true
	}

	if !changed {
		return false, nil
	}

	if err := writeConfigTOMLAtomicMode(path, cfg, info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

// writeConfigTOMLAtomicMode serializes cfg to dest atomically (tempfile + rename
// in the same directory), preserving the given file mode. It uses the SAME
// orderedConfig key ordering as writeConfigTOMLAtomic / writeConfigTOMLTo:
// [workspace] first, [repos.*], then [hooks] (omitted when empty).
func writeConfigTOMLAtomicMode(dest string, cfg *WorkspaceConfig, mode os.FileMode) error {
	type orderedConfig struct {
		Workspace WorkspaceSection      `toml:"workspace"`
		Repos     map[string]RepoConfig `toml:"repos"`
		Hooks     []EventHook           `toml:"hooks,omitempty"`
	}
	out := orderedConfig{
		Workspace: cfg.Workspace,
		Repos:     cfg.Repos,
		Hooks:     cfg.Hooks,
	}
	data, err := toml.Marshal(out)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".pn-config-*.tmp")
	if err != nil {
		return fmt.Errorf("write config (tempfile): %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write config (write): %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write config (close): %w", err)
	}
	// CreateTemp makes the file 0600; enforce the requested mode explicitly so
	// the rename lands with the original permissions preserved.
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write config (chmod): %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write config (rename): %w", err)
	}
	return nil
}
