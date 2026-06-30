package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"
)


const (
	// ConfigFileName is the workspace TOML filename at the workspace root.
	ConfigFileName = "pn-workspace.toml"
)

// Workspace is the in-memory representation of a workspace rooted at Root.
type Workspace struct {
	root    string
	config  *WorkspaceConfig
	lock    *Lock
	revLock *RevLock
	runner  exec.Runner
	pool    *exec.WorkerPool
	// registerChecksFn overrides the default check registry. nil in production;
	// set only in tests to stub the re-run inside applyFixes.
	registerChecksFn func() []check
}

// Open loads the workspace rooted at dir. Reads pn-workspace.toml (required),
// pn-workspace.lock.json (optional, DAG ordering), and pn-workspace.revs.json
// (optional, per-repo URL+Rev for reproducibility). Constructs a shared
// WorkerPool sized to runtime.NumCPU() for per-repo subprocess fan-out;
// callers should call Close() when finished to drain the pool.
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
		return nil, fmt.Errorf("%w (run `pn workspace lock` to regenerate)", err)
	}
	revLock, err := ReadRevLock(filepath.Join(dir, RevLockFileName))
	if err != nil {
		return nil, err
	}
	pool := exec.NewWorkerPool(runner, runtime.NumCPU())
	return &Workspace{
		root:    dir,
		config:  cfg,
		lock:    lock,
		revLock: revLock,
		runner:  runner,
		pool:    pool,
	}, nil
}

// Close releases the workspace's resources (worker pool, etc.).
// Safe to call multiple times.
func (w *Workspace) Close() {
	if w.pool != nil {
		w.pool.Close()
	}
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

// WorkforestsDir returns the absolute path of the configured workforests directory.
// Relative values from workforests_dir are resolved against the workspace root;
// absolute values are returned as-is. Defaults to <root>/.workforests when unset.
func (w *Workspace) WorkforestsDir() string {
	name := w.config.WorkforestsDirName()
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(w.root, name)
}
