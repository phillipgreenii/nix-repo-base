// Package config loads pg-go-mutate-tui's two configuration surfaces:
//
//   - StaticConfig: nix-rendered, read-only settings written by the
//     home-manager module to a fixed path (`${XDG_CONFIG_HOME}/pg-go-mutate-tui/config.json`).
//     Unknown top-level keys are logged and ignored, never treated as an error,
//     so the tool tolerates being run against a newer or older rendered config.
//   - DynamicState: tool-owned state (e.g. which projects are currently
//     included) that the TUI itself reads and rewrites across runs.
//
// JSON keys use camelCase (scanPaths, lowWatermark, ...) to match the
// attrset keys a home-manager module naturally produces via
// `pkgs.formats.json {}` — see the nix wiring task's documented settings
// keys (`scanPaths`, `concurrency`, `lowWatermark`, `highWatermark`,
// `refillRetrySeconds`, `repoLabels`) in the design plan.
package config

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// StaticConfig holds the nix-rendered, read-only settings for pg-go-mutate-tui.
type StaticConfig struct {
	ScanPaths     []string
	Concurrency   int
	LowWatermark  int
	HighWatermark int
	RefillRetry   time.Duration
	RepoLabels    map[string]string
}

// defaultStaticConfig returns the built-in defaults applied when a key is
// absent from the rendered config (or the file itself is absent).
func defaultStaticConfig() StaticConfig {
	return StaticConfig{
		Concurrency:   1,
		LowWatermark:  20,
		HighWatermark: 80,
		RefillRetry:   5 * time.Minute,
	}
}

// knownStaticKeys is the set of top-level JSON keys LoadStatic understands.
// Any other top-level key present in the file is logged via logger.Warn but
// otherwise ignored.
var knownStaticKeys = map[string]struct{}{
	"scanPaths":          {},
	"concurrency":        {},
	"lowWatermark":       {},
	"highWatermark":      {},
	"refillRetrySeconds": {},
	"repoLabels":         {},
}

// staticConfigJSON mirrors StaticConfig's known keys for unmarshaling.
// RefillRetrySeconds is a plain int (seconds) on the wire; LoadStatic
// converts it to a time.Duration.
type staticConfigJSON struct {
	ScanPaths          []string          `json:"scanPaths"`
	Concurrency        int               `json:"concurrency"`
	LowWatermark       int               `json:"lowWatermark"`
	HighWatermark      int               `json:"highWatermark"`
	RefillRetrySeconds int               `json:"refillRetrySeconds"`
	RepoLabels         map[string]string `json:"repoLabels"`
}

// LoadStatic reads the nix-rendered static config at path. A missing file is
// not an error: it returns the built-in defaults with a nil error. Unknown
// top-level keys within an existing file are logged via logger.Warn and
// otherwise ignored — they never cause LoadStatic to fail. Known keys absent
// from the file keep their built-in default rather than becoming a
// zero-value, since defaults are applied to the JSON representation before
// unmarshaling over them.
func LoadStatic(path string, logger *slog.Logger) (StaticConfig, error) {
	def := defaultStaticConfig()

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return def, nil
		}
		return StaticConfig{}, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	// Warn on any unknown top-level key without letting it fail the load.
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return StaticConfig{}, err
	}
	for key := range topLevel {
		if _, ok := knownStaticKeys[key]; !ok {
			logger.Warn("pg-go-mutate-tui: ignoring unknown config key", "key", key, "path", path)
		}
	}

	// Seed the JSON representation with the defaults BEFORE unmarshaling the
	// file over it, so an absent known key keeps its default instead of
	// becoming a zero value.
	wire := staticConfigJSON{
		ScanPaths:          def.ScanPaths,
		Concurrency:        def.Concurrency,
		LowWatermark:       def.LowWatermark,
		HighWatermark:      def.HighWatermark,
		RefillRetrySeconds: int(def.RefillRetry / time.Second),
		RepoLabels:         def.RepoLabels,
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return StaticConfig{}, err
	}

	return StaticConfig{
		ScanPaths:     wire.ScanPaths,
		Concurrency:   wire.Concurrency,
		LowWatermark:  wire.LowWatermark,
		HighWatermark: wire.HighWatermark,
		RefillRetry:   time.Duration(wire.RefillRetrySeconds) * time.Second,
		RepoLabels:    wire.RepoLabels,
	}, nil
}

// DynamicState holds tool-owned state that the TUI reads and rewrites across
// runs, independent of the nix-rendered StaticConfig.
type DynamicState struct {
	IncludedProjects map[string]bool `json:"includedProjects"`
}

// LoadDynamic reads dynamic state from path as a plain JSON round trip.
func LoadDynamic(path string) (DynamicState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DynamicState{}, err
	}
	var s DynamicState
	if err := json.Unmarshal(raw, &s); err != nil {
		return DynamicState{}, err
	}
	return s, nil
}

// SaveDynamic writes dynamic state to path as a plain JSON round trip,
// creating parent directories as needed.
func SaveDynamic(path string, s DynamicState) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
