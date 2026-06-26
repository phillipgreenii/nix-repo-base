package store

import (
	"os"
	"path/filepath"
)

// Env captures the filesystem roots store commands read from. It is the
// testability seam: production uses RealEnv(); tests inject temp dirs.
type Env struct {
	Home          string // $HOME
	XDGConfigHome string // $XDG_CONFIG_HOME (may be empty)
	TMPDIR        string // $TMPDIR (may be empty → /tmp)
}

// RealEnv reads the ambient environment.
func RealEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), TMPDIR: os.Getenv("TMPDIR")}
}

// configHome returns XDG_CONFIG_HOME or $HOME/.config.
func (e Env) configHome() string {
	if e.XDGConfigHome != "" {
		return e.XDGConfigHome
	}
	return filepath.Join(e.Home, ".config")
}

// tmpDir returns TMPDIR or /tmp.
func (e Env) tmpDir() string {
	if e.TMPDIR != "" {
		return e.TMPDIR
	}
	return "/tmp"
}
