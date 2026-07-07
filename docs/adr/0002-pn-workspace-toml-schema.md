# ADR-0002: pn-workspace.toml schema for multi-repo workspace management

**Date:** 2026-06-01
**Status:** Accepted (amended by [ADR-0012](0012-pn-applied-state-store-and-info-api.md); hooks superseded by [ADR-0019](0019-per-repo-event-hooks.md))
**Deciders:** phillipgreenii

## Context

Phase 1 needs a declarative way to define a multi-repo workspace.
The pn-workspace-\* tools need a config file to know which repos are in the workspace
and what hooks to run around workspace commands.

## Decision

- pn-workspace.toml lives at workspace root (machine-local, not inside any repo)
- [workspace] section: name, description, id (slug `^[a-z0-9][a-z0-9-]*$`, machine-invariant; the wsid used by `pn:applied` gates — see ADR-0012)
- [repos.<key>] table-of-tables: url (flake URL), optional branch (default: "main")
- [hooks.<command>] section: pre and post arrays of command strings
  - Commands: apply, build, flake-check, init, pre-commit-check, push, rebase, status, update, upgrade
  - Path resolution: /foo = absolute; ./foo = file-relative to TOML; bare name = PATH lookup
  - Failure semantics: pre non-zero aborts; post non-zero warns but does not change exit status
  - No when clauses (machine-local file; users edit for their machine)

## Consequences

Each pn-workspace-\* command calls RunHooks before/after its work.
The hook mechanism generalizes platform-specific gating (e.g., pn-osx-tcc-check)
to user-configurable TOML entries.

Reference: docs/superpowers/specs/2026-05-31-monorepo-nix-refactor-phase-1-design.md §4.2
