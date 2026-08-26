// Package obslog is pg-go-mutate-tui's single import point for structured
// logging, wrapping x/jsonllogger so no other package here needs to know
// that library's exact API.
package obslog

import (
	"log/slog"
	"os"

	"github.com/phillipgreenii/x/jsonllogger"
)

// New returns a *slog.Logger writing ADR-0038-shaped structured JSONL to
// ${XDG_STATE_HOME}/pg-go-mutate-tui/pg-go-mutate-tui.jsonl (falling back to
// $HOME/.local/state when unset), via x/jsonllogger. Unlike jsonllogger.New,
// this never returns an error: if the log file cannot be opened (e.g. an
// unwritable state directory), New falls back to a stderr logger with the
// same field normalization, rather than pushing that failure onto every
// caller just to write a log line.
func New() *slog.Logger {
	l, err := jsonllogger.New("pg-go-mutate-tui")
	if err != nil {
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: jsonllogger.ReplaceAttr}))
	}
	return l
}
