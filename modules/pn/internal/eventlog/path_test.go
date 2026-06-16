package eventlog

import (
	"path/filepath"
	"testing"
)

func TestDefaultPath_usesXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := DefaultPath(), "/xdg/state/pn/events.jsonl"; got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPath_fallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/me")
	if got, want := DefaultPath(), filepath.Join("/home/me", ".local/state/pn/events.jsonl"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
