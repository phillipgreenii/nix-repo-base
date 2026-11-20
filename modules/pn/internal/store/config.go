package store

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config is the parsed ~/.config/pn/store.toml. Defaults: KeepDays 14, KeepCount 3.
type Config struct {
	SearchDirs []string
	KeepDays   int
	KeepCount  int
	// PostGCClearDirs are directories removed (rm -rf) after the GC finishes,
	// for caches that may hold references into store paths the GC just deleted.
	// A leading "~" or "~/" expands to $HOME; anything else must be absolute.
	PostGCClearDirs []string
	// PostGCCommands are argv-form commands run (in order) after the GC and
	// after PostGCClearDirs. A failing command is reported but does not stop
	// the remaining ones.
	PostGCCommands [][]string
	// OptimiseMinFreeGB is the free space /nix must have before the optional
	// optimise step starts (default 10).
	OptimiseMinFreeGB int
	// Devbox enables Devbox discovery (global, util and per-project profiles)
	// in deepclean and audit. Default false.
	Devbox bool
	// Flox enables Flox reporting and temp-dir pruning in deepclean. Default
	// false. Audit has no Flox section, so it has no effect there.
	Flox bool
}

// disabledNote is the one line printed under the header of a section whose
// tool toggle (key is "devbox" or "flox") is off in store.toml.
func disabledNote(key string) string {
	return "  (disabled; set " + key + " = true in ~/.config/pn/store.toml to enable)"
}

// categoryEnabled reports whether a prune-count category is active under the
// config's tool toggles.
func categoryEnabled(cat string, cfg Config) bool {
	switch cat {
	case "devbox-global", "devbox-util", "devbox-projects":
		return cfg.Devbox
	case "flox-temp-dirs":
		return cfg.Flox
	}
	return true
}

type rawConfig struct {
	SearchDirs        []string   `toml:"search_dirs"`
	KeepDays          *int       `toml:"keep_days"`
	KeepCount         *int       `toml:"keep_count"`
	PostGCClearDirs   []string   `toml:"post_gc_clear_dirs"`
	PostGCCommands    [][]string `toml:"post_gc_commands"`
	OptimiseMinFreeGB *int       `toml:"optimise_min_free_gb"`
	Devbox            *bool      `toml:"devbox"`
	Flox              *bool      `toml:"flox"`
}

// defaultConfig returns the built-in defaults (14d / 3 / no search dirs).
func defaultConfig() Config {
	return Config{KeepDays: 14, KeepCount: 3, OptimiseMinFreeGB: DefaultOptimiseMinFreeGB}
}

// parseStoreConfig parses store.toml bytes, applying defaults for absent keys.
// Malformed TOML falls back to defaults (best-effort, matching the bash which
// tolerated yq failures). Split from file I/O so it is unit-testable on
// literals — mirrors workspace.ParseConfig([]byte).
func parseStoreConfig(data []byte) Config {
	c := defaultConfig()
	var raw rawConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return c
	}
	c.SearchDirs = raw.SearchDirs
	c.PostGCClearDirs = raw.PostGCClearDirs
	c.PostGCCommands = raw.PostGCCommands
	if raw.KeepDays != nil {
		c.KeepDays = *raw.KeepDays
	}
	if raw.KeepCount != nil {
		c.KeepCount = *raw.KeepCount
	}
	if raw.OptimiseMinFreeGB != nil {
		c.OptimiseMinFreeGB = *raw.OptimiseMinFreeGB
	}
	if raw.Devbox != nil {
		c.Devbox = *raw.Devbox
	}
	if raw.Flox != nil {
		c.Flox = *raw.Flox
	}
	return c
}

// LoadConfig reads <configHome>/pn/store.toml and parses it. A missing file
// yields defaults.
func LoadConfig(env Env) Config {
	path := filepath.Join(env.configHome(), "pn", "store.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultConfig()
	}
	return parseStoreConfig(data)
}
