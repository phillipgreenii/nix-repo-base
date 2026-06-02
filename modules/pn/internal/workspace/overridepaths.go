package workspace

import (
	"fmt"
	"os"
	"strings"
)

const overridePathsEnv = "PN_WORKSPACE_OVERRIDE_PATHS"

// parseOverridePaths builds a map of repo-key -> absolute override path. Entries
// come from the PN_WORKSPACE_OVERRIDE_PATHS env var (comma-separated, lower
// precedence) and then the given specs (higher precedence). Each entry is
// "name=path"; path must be absolute.
func parseOverridePaths(specs []string) (map[string]string, error) {
	out := map[string]string{}
	add := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return fmt.Errorf("invalid override spec (expected name=path): %s", raw)
		}
		name := strings.TrimSpace(raw[:eq])
		path := strings.TrimSpace(raw[eq+1:])
		if name == "" {
			return fmt.Errorf("invalid override spec (empty name): %s", raw)
		}
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("override path must be absolute: %s", path)
		}
		out[name] = path
		return nil
	}
	if env := os.Getenv(overridePathsEnv); env != "" {
		for _, e := range strings.Split(env, ",") {
			if err := add(e); err != nil {
				return nil, err
			}
		}
	}
	for _, s := range specs {
		if err := add(s); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ParseOverridePaths is the exported entry point for CLI flag parsing.
func ParseOverridePaths(specs []string) (map[string]string, error) { return parseOverridePaths(specs) }
