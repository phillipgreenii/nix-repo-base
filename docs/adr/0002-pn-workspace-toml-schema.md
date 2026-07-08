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
- **Hooks — SUPERSEDED by [ADR-0019](0019-per-repo-event-hooks.md).** As originally
  decided here, hooks were `[hooks.<command>]` tables with `pre`/`post` arrays run once at the
  workspace root. ADR-0019 replaced that with event-hook **lists** — `[[hooks]]` (workspace-scoped)
  and `[[repos.<key>.hooks]]` (per-repo), each `{ when = [<pre|post>-<command>…], run = […] }` — so
  see ADR-0019 for the current shape and semantics. The path-resolution (`/foo` absolute, `./foo`
  file-relative, bare name = PATH) and failure semantics (`pre` non-zero aborts; `post` non-zero
  warns) carry over unchanged; the command set is now any hookable pn-workspace command.

## Consequences

Each pn-workspace-\* command calls RunHooks before/after its work.
The hook mechanism generalizes platform-specific gating (e.g., pn-osx-tcc-check)
to user-configurable TOML entries.

Reference: docs/superpowers/specs/2026-05-31-monorepo-nix-refactor-phase-1-design.md §4.2
