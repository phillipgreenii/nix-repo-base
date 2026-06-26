package jira

import (
	"errors"
	"io/fs"
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the non-secret tool configuration. No tenant default lives here.
type Config struct {
	BaseURL      string       `toml:"base_url"`
	Email        string       `toml:"email"`
	DefaultLimit int          `toml:"default_limit"`
	Secret       SecretConfig `toml:"secret"`
}

// DefaultConfig is the generic, tenant-free baseline.
func DefaultConfig() Config {
	return Config{DefaultLimit: 100, Secret: SecretConfig{Source: "env", EnvVar: "JIRA_API_TOKEN"}}
}

// LoadFile parses a TOML config. A missing file yields a zero Config and no error.
func LoadFile(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Merge returns base with every non-zero field of over applied on top.
func (base Config) Merge(over Config) Config {
	out := base
	if over.BaseURL != "" {
		out.BaseURL = over.BaseURL
	}
	if over.Email != "" {
		out.Email = over.Email
	}
	if over.DefaultLimit != 0 {
		out.DefaultLimit = over.DefaultLimit
	}
	if over.Secret.Source != "" {
		out.Secret.Source = over.Secret.Source
	}
	if over.Secret.EnvVar != "" {
		out.Secret.EnvVar = over.Secret.EnvVar
	}
	if over.Secret.Path != "" {
		out.Secret.Path = over.Secret.Path
	}
	if len(over.Secret.Command) != 0 {
		out.Secret.Command = over.Secret.Command
	}
	return out
}
