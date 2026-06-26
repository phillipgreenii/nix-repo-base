// Package store implements the pn store maintenance commands
// (audit, deepclean) that operate on the Nix store and profile generations.
package store

import "github.com/phillipgreenii/nix-repo-base/modules/pn/internal/exec"

// Store is the entry point for nix store maintenance commands.
type Store struct {
	runner exec.Runner
	env    Env
}

// New returns a Store using the given Runner and the real environment.
func New(runner exec.Runner) *Store {
	return &Store{runner: runner, env: RealEnv()}
}

// NewWithEnv returns a Store with an explicit Env (tests).
func NewWithEnv(runner exec.Runner, env Env) *Store {
	return &Store{runner: runner, env: env}
}

// Runner returns the configured subprocess runner.
func (s *Store) Runner() exec.Runner { return s.runner }

// Env returns the configured environment.
func (s *Store) Env() Env { return s.env }
