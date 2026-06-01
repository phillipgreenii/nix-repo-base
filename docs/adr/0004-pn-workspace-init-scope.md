# ADR-0004: pn-workspace-init scope: clone, lock, reconcile (no agents.md or beads-wiring)

**Date:** 2026-06-01
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

pn-workspace-init needs a bounded scope for Phase 1. The full vision includes
agents.md integration and beads-wiring, but Phase 1 needs a tractable deliverable.

## Decision

Phase 1 scope: three behaviors, all always-on:

1. Clone repos from TOML to `$WORKSPACE_ROOT/<key>/` (idempotent: skip if present)
2. Generate pn-workspace.lock at workspace root with resolved revs
3. Reconcile existing clones: if a repo dir exists but is absent from TOML, ADD it to TOML

Out of scope for Phase 1: agents.md generation, beads-wiring, workspace-scoped Claude config

## Consequences

pn-workspace-init is useful for initial workspace setup and drift detection
but does not yet configure agent tooling. That is deferred to Phase 4.

Reference: docs/superpowers/specs/2026-05-31-monorepo-nix-refactor-phase-1-design.md §4.2
