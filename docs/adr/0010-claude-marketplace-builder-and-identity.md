# ADR-0010: `mkClaudeMarketplace` builder + local-marketplace identity convention

**Date:** 2026-06-24
**Status:** Accepted
**Deciders:** phillipgreenii
**Amends:** [ADR-0003](0003-claude-marketplace-convention.md) (fulfils its "Phase 4 auto-install")

## Context

ADR-0003 established that each `nix-*` repo ships a Claude marketplace at its
root (`.claude-plugin/marketplace.json` + per-plugin dirs) and that Phase 4 would
automate installation via nix-agent-support. Until now there was no builder: the
marketplace was a static directory with no nix artifact, no content-derived
version stamping, and no declarative registration path. `pn-workspace-rules` was
registered in no nix module and not delivered to agents.

We also need a stable naming scheme so a nix-built local marketplace does not
collide with a hand-`add`ed one, and so the registration key, manifest name, and
`enabledPlugins` keys all agree regardless of which Claude keys on.

## Decision

### Builder family in repo-base

`lib/claude-marketplace.nix` provides the factory
`mkClaudeMarketplaceBuilders { pkgs, lib }` (peer of `mkBashBuilders` /
`mkGoBuilders`, exposed un-applied on `flake.lib`) returning:

- `mkClaudePlugin { src }` — bundle one plugin dir, stamping its `plugin.json`
  `version` to `<declared>+<digest>`.
- `mkClaudeMarketplace { src, nameSuffix ? "-local" }` — bundle an in-repo
  marketplace; regenerate `marketplace.json` with the suffixed identity and
  per-plugin stamped versions; expose `passthru = { marketplaceName; plugins; }`.
- `mkDirectoryMarketplaceSettings { marketplace, path, enabled ? {} }` — pure
  helper producing the `settings.json` fragment (`extraKnownMarketplaces`
  directory source + `enabledPlugins` + `plugins`).

The factory takes **no `self`**: versions are content-derived, never repo-rev
(ADR-0006). The per-plugin digest is `mkSrcDigest (builtins.path { path = <plugin>; … })`
— content-addressed, so it changes iff that plugin's content changes and is
stable for unrelated repo edits.

### Identity convention

- Installed identity = **`<repo>-marketplace<nameSuffix>`**, `nameSuffix` default
  `-local` (aligns with `beads-marketplace` / `superpowers-marketplace`; `-local`
  marks the nix variant). For this repo: `phillipg-nix-repo-base-marketplace-local`.
- The checked-in source `marketplace.json` keeps the **bare repo name**
  (`phillipg-nix-repo-base`), so `claude plugin marketplace add <repo>` still works
  for a direct/manual install without colliding with the `-local` nix install.
- Manifest `name`, `extraKnownMarketplaces` key, and `enabledPlugins` keys are all
  the same suffixed string ⇒ resolves correctly however Claude keys it.

### Version form (verified)

A plugin version is `<declared>+<digest>` in the manifest and on disk. A directory
plugin is copied into `~/.claude/plugins/cache/<mkt>/<plugin>/<version>/`; a version
change re-copies (cache-busts). Verified empirically on Claude Code 2.1.186: a bump
of the digest portion is recognized as a version change and serves new content. The
`+` is sanitized to `-` only in the cache **path**; the change is still detected. No
`-<digest>` fallback is needed.

## Consequences

- `mkClaudeMarketplace` fulfils ADR-0003's "Phase 4 auto-install" — the builder
  produces the artifact; registration is a separate consumer-layer concern owned by
  nix-agent-support's single `claude-settings` writer.
- Other `nix-*` repos follow the same convention (Pattern 1 in
  `docs/claude-marketplaces.md`).
- ADR-0003 is updated to **Amended by 0010**; its "Phase 1 manual install" remains
  the historical record.

Reference: `docs/superpowers/plans/2026-06-24-claude-marketplaces-plan.md`,
usage guide `docs/claude-marketplaces.md`.
