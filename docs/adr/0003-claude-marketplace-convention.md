# ADR-0003: Claude Code marketplace convention for nix-\* repos

**Date:** 2026-06-01
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

Phase 1 adds pn-workspace-rules as a Claude Code plugin. We need a convention
for how nix-\* repos expose Claude plugins.

## Decision

- Each nix-\* repo that exposes Claude plugins ships a valid Claude marketplace at repo root
- Structure: `<repo>/.claude-plugin/marketplace.json` + per-plugin dirs
- marketplace.json follows Claude Code's native schema; lists plugins; owner is phillipgreenii
- Plugin content: `<plugin-name>/.claude-plugin/plugin.json` + `<plugin-name>/CLAUDE.md`
- Installation: manual via `claude plugin marketplace add <path>` in Phase 1
- Phase 4 will automate installation via nix-agent-support's unified marketplaces config

## Consequences

Phase 1 installs pn-workspace-rules@pn-workspace manually on monorepod.
Phase 4 adds auto-install. Other nix-\* repos follow this convention when adding Claude plugins.

Reference: docs/superpowers/specs/2026-05-31-monorepo-nix-refactor-phase-1-design.md §5
