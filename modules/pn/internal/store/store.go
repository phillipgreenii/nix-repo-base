// Package store implements the pn store maintenance commands
// (audit, deepclean) that operate on the Nix store and profile generations.
package store

import "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"

// Store is the entry point for nix store maintenance commands.
type Store struct {
	runner exec.Runner
}

// New returns a Store using the given Runner.
func New(runner exec.Runner) *Store {
	return &Store{runner: runner}
}

// Runner returns the configured subprocess runner.
func (s *Store) Runner() exec.Runner { return s.runner }
