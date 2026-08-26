package obslog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewWritesToXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	logger := New()
	logger.Info("test message")
	path := filepath.Join(dir, "pg-go-mutate-tui", "pg-go-mutate-tui.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected log file at %s: %v", path, err)
	}
}

func TestNewFallsBackToStderrWhenStateDirIsUnwritable(t *testing.T) {
	dir := t.TempDir()
	// jsonllogger.New("pg-go-mutate-tui") MkdirAll's dir/pg-go-mutate-tui; putting
	// a regular file there makes that MkdirAll fail, exercising New's fallback.
	blocked := filepath.Join(dir, "pg-go-mutate-tui")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatalf("write blocking file %s: %v", blocked, err)
	}
	t.Setenv("XDG_STATE_HOME", dir)

	logger := New() // must not panic despite the underlying jsonllogger.New error
	if logger == nil {
		t.Fatal("New() returned a nil logger")
	}
	logger.Info("still logs somewhere, not to the blocked path")
}
