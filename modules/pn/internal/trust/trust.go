// Package trust implements a direnv-style trust-on-first-use (TOFU) allowlist
// for pn workspaces. A workspace's [[hooks]] execute `sh -c` from a
// pn-workspace.toml discovered by walking up from the cwd, so merely `cd`-ing
// into an untrusted checkout could run attacker-controlled code on the next
// hookable `pn workspace` command. This package records, per absolute workspace
// root, the SHA-256 of its pn-workspace.toml; hook execution is gated on a
// matching record. Editing the TOML re-blocks until re-allowed. See bead
// pg2-oymai.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// configFileName is the workspace config whose content is hashed into the trust
// record. Duplicated here (not imported from internal/workspace) to keep this a
// dependency-free leaf package and avoid an import cycle.
const configFileName = "pn-workspace.toml"

// ErrUntrusted is wrapped by EnsureAllowed's errors so callers/tests can
// errors.Is against it.
var ErrUntrusted = errors.New("workspace hooks not trusted")

// record is the on-disk trust record (JSON).
type record struct {
	Root         string `json:"root"`
	ConfigSHA256 string `json:"config_sha256"`
	AllowedAt    string `json:"allowed_at"`
}

// stateDir returns ${XDG_STATE_HOME}/pn/trust, falling back to
// ~/.local/state/pn/trust (matching the internal/eventlog convention).
func stateDir() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(state, "pn", "trust")
}

// recordPath returns the per-root record file path, keyed by the SHA-256 of the
// absolute root path.
func recordPath(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(stateDir(), hex.EncodeToString(sum[:])), nil
}

// configSHA returns the hex SHA-256 of the workspace's pn-workspace.toml.
func configSHA(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, configFileName))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// IsAllowed reports whether root is trusted AND its pn-workspace.toml is
// unchanged since it was trusted. A missing record or a content-hash mismatch
// returns (false, nil); only genuine I/O/parse errors are returned.
func IsAllowed(root string) (bool, error) {
	rp, err := recordPath(root)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(rp)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return false, err
	}
	cur, err := configSHA(root)
	if err != nil {
		return false, err
	}
	return rec.ConfigSHA256 == cur, nil
}

// EnsureAllowed returns nil when root is trusted and unchanged; otherwise an
// actionable error wrapping ErrUntrusted, distinguishing never-trusted from
// changed-since-trusted.
func EnsureAllowed(root string) error {
	rp, err := recordPath(root)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(rp)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace hooks in %s are not trusted; review it and run: pn workspace allow: %w", root, ErrUntrusted)
	}
	if err != nil {
		return fmt.Errorf("trust: read record: %w", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("trust: parse record %s: %w", rp, err)
	}
	cur, err := configSHA(root)
	if err != nil {
		return err
	}
	if rec.ConfigSHA256 != cur {
		return fmt.Errorf("pn-workspace.toml in %s changed since it was trusted; re-review and run: pn workspace allow: %w", root, ErrUntrusted)
	}
	return nil
}

// Allow records trust for root, hashing its current pn-workspace.toml. The
// state dir is created 0o700 and the record written 0o600 (atomically via
// tempfile+rename) so a co-tenant cannot pre-seed or read a trust record.
func Allow(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	sha, err := configSHA(root)
	if err != nil {
		return err
	}
	rec := record{
		Root:         abs,
		ConfigSHA256: sha,
		AllowedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rp, err := recordPath(root)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trust-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, rp); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Deny removes root's trust record. A missing record is a no-op.
func Deny(root string) error {
	rp, err := recordPath(root)
	if err != nil {
		return err
	}
	if err := os.Remove(rp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
