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
}

type rawConfig struct {
	SearchDirs []string `toml:"search_dirs"`
	KeepDays   *int     `toml:"keep_days"`
	KeepCount  *int     `toml:"keep_count"`
}

// defaultConfig returns the built-in defaults (14d / 3 / no search dirs).
func defaultConfig() Config { return Config{KeepDays: 14, KeepCount: 3} }

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
	if raw.KeepDays != nil {
		c.KeepDays = *raw.KeepDays
	}
	if raw.KeepCount != nil {
		c.KeepCount = *raw.KeepCount
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
