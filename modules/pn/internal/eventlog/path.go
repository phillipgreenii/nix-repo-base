package eventlog

import (
	"os"
	"path/filepath"
)

// DefaultPath returns the standard JSONL event-log path
// ${XDG_STATE_HOME}/pn/events.jsonl, falling back to ~/.local/state when
// XDG_STATE_HOME is unset (matching the project convention).
func DefaultPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(os.Getenv("HOME"), ".local/state")
	}
	return filepath.Join(state, "pn", "events.jsonl")
}
