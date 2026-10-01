package telemetrycfg

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NixStorePrefix is the only tree a wrapper may live in when pn runs it as root.
const NixStorePrefix = "/nix/store/"

// ValidateSudoWrapper decides whether path may be executed under sudo. The
// resolved real path (EvalSymlinks) MUST be under /nix/store: a user-writable
// telemetry.toml names wrapper_path, so without this guard it could make sudo
// execute an arbitrary binary. There is deliberately NO environment override.
// It returns the resolved real path on success. evalSymlinks is injectable
// (filepath.EvalSymlinks in production).
func ValidateSudoWrapper(path string, evalSymlinks func(string) (string, error)) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no wrapper path configured")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("wrapper path %q is not absolute", path)
	}
	if evalSymlinks == nil {
		evalSymlinks = filepath.EvalSymlinks
	}
	real, err := evalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve wrapper path %q: %w", path, err)
	}
	real = filepath.Clean(real)
	if !strings.HasPrefix(real, NixStorePrefix) {
		return "", fmt.Errorf("wrapper %q resolves to %q, outside %s; refusing to run it under sudo", path, real, NixStorePrefix)
	}
	return real, nil
}
