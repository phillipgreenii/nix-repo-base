package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)

const (
	// ConfigFileName is the workspace TOML filename at the workspace root.
	ConfigFileName = "pn-workspace.toml"
	// LockFileName is the workspace lock filename at the workspace root.
	LockFileName = "pn-workspace.lock"
)

// Workspace is the in-memory representation of a workspace rooted at Root.
type Workspace struct {
	root    string
	config  *WorkspaceConfig
	lock    *Lock
	revLock *RevLock
	runner  exec.Runner
}

// Open loads the workspace rooted at dir. Reads pn-workspace.toml (required),
// pn-workspace.lock (optional, DAG ordering), and pn-workspace.revs.json
// (optional, per-repo URL+Rev for reproducibility). Returns an error if the
// TOML is missing or malformed.
func Open(dir string, runner exec.Runner) (*Workspace, error) {
	cfgPath := filepath.Join(dir, ConfigFileName)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, err
	}
	lock, err := ReadLock(filepath.Join(dir, LockFileName))
	if err != nil {
		return nil, err
	}
	revLock, err := ReadRevLock(filepath.Join(dir, RevLockFileName))
	if err != nil {
		return nil, err
	}
	return &Workspace{
		root:    dir,
		config:  cfg,
		lock:    lock,
		revLock: revLock,
		runner:  runner,
	}, nil
}

// Root returns the workspace root directory.
func (w *Workspace) Root() string { return w.root }

// Config returns the parsed workspace config.
func (w *Workspace) Config() *WorkspaceConfig { return w.config }

// Lock returns the parsed DAG lock state.
func (w *Workspace) Lock() *Lock { return w.lock }

// RevLock returns the parsed per-repo URL+Rev lock state.
func (w *Workspace) RevLock() *RevLock { return w.revLock }

// Runner returns the workspace's subprocess runner.
func (w *Workspace) Runner() exec.Runner { return w.runner }
