package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStaticAppliesDefaultsWhenFileAbsent(t *testing.T) {
	cfg, err := LoadStatic(filepath.Join(t.TempDir(), "missing.json"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 1 || cfg.LowWatermark != 20 || cfg.HighWatermark != 80 {
		t.Fatalf("expected built-in defaults, got %+v", cfg)
	}
}

func TestLoadStaticWarnsOnUnknownKeyWithoutFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"concurrency": 3, "totally_unknown_key": true}`), 0o644)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg, err := LoadStatic(path, logger)
	if err != nil {
		t.Fatalf("unknown key must not cause a load failure, got: %v", err)
	}
	if cfg.Concurrency != 3 {
		t.Fatalf("known keys must still apply, got %+v", cfg)
	}
	if !bytes.Contains(buf.Bytes(), []byte("totally_unknown_key")) {
		t.Fatal("expected a warn log naming the unknown key")
	}
}

func TestDynamicStateRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := DynamicState{IncludedProjects: map[string]bool{"proj-a": true, "proj-b": false}}
	if err := SaveDynamic(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDynamic(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.IncludedProjects["proj-a"] != true || got.IncludedProjects["proj-b"] != false {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
