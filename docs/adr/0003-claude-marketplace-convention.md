# ADR-0003: Claude Code marketplace convention for nix-\* repos

**Date:** 2026-06-01
**Status:** Accepted (amended by [ADR-0010](0010-claude-marketplace-builder-and-identity.md))
**Deciders:** phillipgreenii

## Context

Phase 1 adds pn-workspace-rules as a Claude Code plugin. We need a convention
for how nix-\* repos expose Claude plugins.

## Decision

- Each nix-\* repo that exposes Claude plugins ships a valid Claude marketplace at repo root
- Structure: `<repo>/.claude-plugin/marketplace.json` + per-plugin dirs
- marketplace.json follows Claude Code's native schema; lists plugins; owner is phillipgreenii
- Plugin content: `<plugin-name>/.claude-plugin/plugin.json` + `<plugin-name>/CLAUDE.md`
  - **Superseded (verified on Claude Code 2.1.186):** a plugin-root `CLAUDE.md` is
    NOT loaded into agent context, and `plugin.json` `type`/`content` fields are
    ignored (flagged as Unknown by `claude plugin validate`). Plugins must ship
    `skills/`, `agents/`, or `hooks/`; rules belong in a `skills/<name>/SKILL.md`
    with `name` + `description` frontmatter. See ADR-0010 and `docs/claude-marketplaces.md`.
- Installation: manual via `claude plugin marketplace add <path>` in Phase 1
- Phase 4 will automate installation via nix-agent-support's unified marketplaces config

## Consequences

Phase 1 installed `pn-workspace-rules@pn-workspace` manually on monorepod.
Phase 4 adds auto-install. Other nix-\* repos follow this convention when adding Claude plugins.

**Amended by [ADR-0010](0010-claude-marketplace-builder-and-identity.md):** the
`mkClaudeMarketplace` builder fulfils the "Phase 4 auto-install" intent, and the
installed marketplace identity is now `<repo>-marketplace-local` (the source
`marketplace.json` `name` is the bare repo name, here `phillipg-nix-repo-base`,
not the old `pn-workspace`). The plugin name `pn-workspace-rules` is unchanged.

Reference: docs/superpowers/specs/2026-05-31-monorepo-nix-refactor-phase-1-design.md §5
