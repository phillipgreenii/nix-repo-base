package app

// Where the shim's opt-in verbs and its root endpoint come from.
//
// Non-root: ~/.config/pn/shim.toml (verbs = [...]) and PG_NIX_SHIM_VERBS (a
// comma list), merged additively. A malformed file reads as absent.
//
// Root (euid 0): only the root configuration file, accepted only when its
// EvalSymlinks result is a regular file under the store prefix and read from
// THAT resolved path, so a swap between check and read cannot redirect it.
// Root never reads anything under a home directory and ignores the variable
// (sudo drops it anyway).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	// DefaultRootConfigPath is the root shim configuration, written by the
	// nix-darwin module as a store symlink.
	DefaultRootConfigPath = "/etc/pg-nix-shim.json"
	// DefaultStorePrefix is where the resolved root configuration must live.
	DefaultStorePrefix = "/nix/store/"
	// ShimTOMLRelPath is the per-user opt-in file, relative to the home directory.
	ShimTOMLRelPath = ".config/pn/shim.toml"
	// ShimVerbsEnv is the non-root comma list of opt-in verbs.
	ShimVerbsEnv = "PG_NIX_SHIM_VERBS"
)

type rootConfig struct {
	RootEndpoint string   `json:"rootEndpoint"`
	Verbs        []string `json:"verbs"`
}

// userVerbs returns the non-root opt-in entries (file, then variable).
func (r *runner) userVerbs() []string {
	var out []string
	if r.Home != "" && r.ReadFile != nil {
		if b, err := r.ReadFile(strings.TrimRight(r.Home, "/") + "/" + ShimTOMLRelPath); err == nil {
			var fc struct {
				Verbs []string `toml:"verbs"`
			}
			if toml.Unmarshal(b, &fc) == nil {
				out = append(out, fc.Verbs...)
			}
		}
	}
	for _, v := range strings.Split(r.getenv(ShimVerbsEnv), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// loadRootConfig reads the root configuration. A non-empty why means the shim
// must pass through, and names the reason.
func (r *runner) loadRootConfig() (rc rootConfig, why string) {
	path, prefix := r.RootConfigPath, r.StorePrefix
	if path == "" {
		path = DefaultRootConfigPath
	}
	if prefix == "" {
		prefix = DefaultStorePrefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return rc, "no root configuration"
	}
	if !strings.HasPrefix(resolved, prefix) {
		return rc, "root configuration is not under the store prefix"
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.Mode().IsRegular() {
		return rc, "root configuration is not a regular file"
	}
	read := r.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	b, err := read(resolved)
	if err != nil {
		return rc, "root configuration unreadable"
	}
	if json.Unmarshal(b, &rc) != nil {
		return rootConfig{}, "root configuration malformed"
	}
	return rc, ""
}
