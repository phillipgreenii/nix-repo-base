package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"
)

// EnforceKeys reconciles the two nix-owned keys in a pn-workspace.toml against
// committed source values: [workspace].id and [hooks.apply].post. It has
// create-if-missing / enforce-when-present semantics and is deliberately narrow:
// it NEVER renders or owns [repos.*] or any other key — pn owns those.
//
// Semantics (all mandated by the pg2-k43p.6 design guardrails):
//   - Absent file → no-op, returns (false, nil). (pn workspace init creates the
//     file; the next apply enforces.)
//   - Loads the file via ParseConfig, sets ONLY Workspace.Id and
//     Hooks["apply"].Post = [applyPost] (creating the hooks map / apply entry
//     when missing), and preserves everything else verbatim by re-marshalling
//     through the SAME orderedConfig struct pn's own writer uses — so output is
//     byte-identical to `pn init` / `doctor --fix`.
//   - Writes only when a value actually differs (idempotent no-op otherwise);
//     returns (true, nil) iff it wrote.
//   - The write is atomic (tempfile + rename in the same dir) and preserves the
//     original file's mode (e.g. 0600).
//   - Rejects an id that is not a slug (^[a-z0-9][a-z0-9-]*$) rather than write
//     an invalid value; the nix layer also asserts this at eval time.
//
// It reuses pn's go-toml/v2 serialization rather than reimplementing TOML
// writing, so nix-driven activation and pn's own commands cannot fight.
func EnforceKeys(path, id, applyPost string) (bool, error) {
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

	// Enforce hooks.apply.post = [applyPost]. Create the apply entry (and the
	// hooks map, though ParseConfig already guarantees it is non-nil) when
	// missing. Never write an empty post list.
	want := []string{applyPost}
	apply := cfg.Hooks["apply"]
	if !reflect.DeepEqual(apply.Post, want) {
		apply.Post = want
		cfg.Hooks["apply"] = apply
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
		Workspace WorkspaceSection       `toml:"workspace"`
		Repos     map[string]RepoConfig  `toml:"repos"`
		Hooks     map[string]HookCommand `toml:"hooks,omitempty"`
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
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write config (write): %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (close): %w", err)
	}
	// CreateTemp makes the file 0600; enforce the requested mode explicitly so
	// the rename lands with the original permissions preserved.
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (chmod): %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write config (rename): %w", err)
	}
	return nil
}
