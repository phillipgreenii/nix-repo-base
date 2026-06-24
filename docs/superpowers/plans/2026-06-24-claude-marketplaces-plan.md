# Plan: `mkClaudeMarketplace` builder + per-repo local marketplace registration (FINAL)

## Goal

Deliver Claude Code plugins to agents declaratively via nix, with:

- a reusable **builder** in `phillipg-nix-repo-base` (peer of `mkBashBuilders`/`mkGoBuilders`) that bundles an in-repo Claude marketplace into the nix store with content-derived version stamping;
- **one marketplace per repo**, registered as a **local directory-source** marketplace (no network), named `<repo>-marketplace<suffix>` (suffix default `-local`);
- per-plugin enable/disable driven by each plugin's `defaultEnabled` (absent ⇒ `false`);
- the actual `~/.claude/settings.json` write owned by a single writer in `phillipgreenii-nix-agent-support`.

Immediate target: get `pn-workspace-rules` (repo-base) actually loading for agents (bead `pg2-7j5j`). Today it is registered in no nix module and not delivered.

## Verification status

- **Version-reload gate: PASSED (empirical, Claude Code 2.1.186).** In an isolated `CLAUDE_CONFIG_DIR`: a directory-source marketplace installed at `1.0.0+aaaaaaaa`, then content + version bumped to `1.0.0+bbbbbbbb`; `claude plugin update` reported "updated from 1.0.0+aaaaaaaa to 1.0.0+bbbbbbbb", created a fresh cache dir, and served the new content. Conclusion: `<declared>+<digest>` is recognized as a version change and busts the cache. The `+` is sanitized to `-` in the cache path (`cache/<mkt>/<plugin>/1.0.0-bbbbbbbb/`) but the change is still detected. **No `-<digest>` fallback needed.** Refresh is triggered by `claude plugin update` (run by agent-support's `install-plugin.sh` activation) or a restart.
- Still to verify during implementation: (a) `mkSrcDigest (builtins.path { path = <plugindir>; })` changes on content edit and is stable for unrelated repo edits (clean + dirty/override); (b) the local edit→rebuild loop through the coordinated-worktree input override; (c) a `type: rules` plugin's CLAUDE.md actually injects into agent context (separate open item — determines whether the plugin does anything).

## Constraints (established + verified)

1. **Dependency direction (no cycle):** agent-support inputs repo-base as `phillipgreenii-nix-base` and already consumes its `lib.{mkBashBuilders,mkGoBuilders,mkSrcDigest}` (flake.nix:79-92), `flakeModules.*` (309-311), `homeModules.install-metadata` (904). repo-base does NOT input agent-support. So repo-base provides builder + artifact + descriptor data; agent-support consumes.
2. **No drop-in auto-discovery:** `known_marketplaces.json`/`installed_plugins.json` are Claude-written/ephemeral; `settings.json` (`extraKnownMarketplaces` + `enabledPlugins` + `plugins`) is the only declarative surface. Directory source = read in place (no clone), `installLocation` = the path itself.
3. **Reload is version-keyed (verified):** directory plugins are copied into `~/.claude/plugins/cache/<mkt>/<plugin>/<version>/`; a version change re-copies. The plugin's own `plugin.json` version wins over the marketplace listing. So the version must change when content changes ⇒ content-digest suffix.
4. **Single settings writer:** agent-support's `claude-settings` module owns `~/.claude/settings.json` (activation: `replace-managed-keys.sh` replaces managed keys, `install-plugin.sh` installs). Nothing else may write it. Registration feeds `phillipgreenii.programs.claude.settings.*` options; the activation does the write.
5. **Layering analogy:** the builder is to a marketplace what `mkGoBinary` is to a binary — it produces an artifact; installing/registering it is a separate, pluggable consumer-layer concern (agent-support option, `claude plugin marketplace add`, managed-settings, hand-written settings). The builder is independently useful (CLI install, CI validation, any consumer) without agent-support.

## Architecture: a builder family in repo-base

New `phillipg-nix-repo-base/lib/claude-marketplace.nix` — factory `mkClaudeMarketplaceBuilders { pkgs, lib }` (NO `self`; versioning is content-digest, never repo-rev — passing `self` invites reintroducing `self.rev` churn that ADR-0006 kills) returning `{ mkClaudePlugin, mkClaudeMarketplace, mkDirectoryMarketplaceSettings }`. The settings helper lives INSIDE the factory because it needs `lib` (`nix/packages.nix` is an arg-less `_:`). Exposed on `flake.lib` like the other builders.

### `mkClaudePlugin { src }`

Bundles one plugin dir (`.claude-plugin/plugin.json` + content), stamping `plugin.json`'s `version` to `<declared>+<digest>` where `<declared>` = the plugin's own manifest version and `<digest> = mkSrcDigest (builtins.path { path = src; name = "<plugin>-src"; })`. The `builtins.path` scopes the digest to the plugin's OWN subtree (no IFD — reads source, not built output; no bump on unrelated repo edits). Returns a derivation carrying `passthru = { pluginName; version; defaultEnabled; }`.

### `mkClaudeMarketplace { src, nameSuffix ? "-local" }`

`src` MUST be a narrowed `lib.fileset.toSource` (the `.claude-plugin/marketplace.json` + the listed plugin dirs), NOT `./.` — passing the whole repo realizes the entire tree into the store (closure bloat: `.git`/docs/Go trees) and makes the marketplace derivation's drvPath depend on every unrelated repo edit (re-realizes the artifact even though per-plugin versions stay stable). Mirror the `lib.fileset.toSource` discipline at agent-support flake.nix:833-841 / repo-base ADR-0008. Reads `src/.claude-plugin/marketplace.json` (its `name` = the repo name). Generated marketplace identity = `${manifest.name}-marketplace${nameSuffix}`. For each listed plugin, stamps a per-plugin `builtins.path`-scoped `<declared>+<digest>` version (mkClaudePlugin semantics) and reads `defaultEnabled` (absent ⇒ false). Assembles `$out` = `.claude-plugin/marketplace.json` (regenerated: suffixed name + stamped versions) + per-plugin dirs (with stamped plugin.json). Derivation name `claude-marketplace-${manifest.name}`. The marketplace derivation itself carries NO repo-rev-dependent version. Passthru:

```
passthru = {
  marketplaceName = "${manifest.name}-marketplace${nameSuffix}";
  plugins = [ { name; key = "${name}@${marketplaceName}"; defaultEnabled; } … ];
};
```

The repo's checked-in `marketplace.json` keeps the bare repo name (direct `marketplace add` shows `<repo>`, won't collide with the `<repo>-marketplace-local` nix install). (Second construction mode `{ name; owner; plugins = [<mkClaudePlugin outputs>] }` for the B4 aggregate — DEFERRED, spec with B4.)

### `mkDirectoryMarketplaceSettings { marketplace, path, enabled ? {} }` (pure)

```nix
{ marketplace, path, enabled ? { } }:
let resolve = p: enabled.${p.key} or enabled.${p.name} or p.defaultEnabled; in
{
  extraKnownMarketplaces.${marketplace.marketplaceName}.source = { source = "directory"; inherit path; };
  enabledPlugins = lib.listToAttrs (map (p: lib.nameValuePair p.key (resolve p)) marketplace.plugins);
  plugins = map (p: p.key) marketplace.plugins;   # all registered; enable flag controls on/off
}
```

Caller supplies only `marketplace` + install `path` (+ optional per-plugin `enabled` overrides). Name + plugin keys ride on passthru.

## Naming convention (uniform for all repos)

- Installed identity = **`<repo>-marketplace<nameSuffix>`**, `nameSuffix` default `-local` (aligns with `beads-marketplace`/`superpowers-marketplace`; `-local` marks the nix variant).
- Source `marketplace.json` `name` = the repo name; builder appends `-marketplace<suffix>`.
- Manifest `name`, `extraKnownMarketplaces` key, and `enabledPlugins` keys are all the same suffixed string ⇒ resolves correctly regardless of whether Claude keys on the registration key or the manifest name.

## repo-base changes

1. `lib/claude-marketplace.nix` — the factory (mkClaudePlugin + mkClaudeMarketplace + mkDirectoryMarketplaceSettings), `{ pkgs, lib }` only. Plus `lib/claude-marketplace-tests.nix` (pure-eval unit tests, version-tests.nix pattern) wired as a `checks.*` entry.
2. `nix/packages.nix` — `mkClaudeMarketplaceBuilders = import ../lib/claude-marketplace.nix;`.
3. `flake.nix` `lib` output — expose the **factory** un-applied: `inherit ((import ./nix/packages.nix { })) mkClaudeMarketplaceBuilders;` (exactly like `mkBashBuilders`/`mkGoBuilders` at flake.nix:209-224; consumers apply it with their own `{ pkgs, lib }`). `mkClaudeMarketplace`/`mkClaudePlugin`/`mkDirectoryMarketplaceSettings` exist only AFTER application — they are NOT exposed directly on `flake.lib`.
4. `flake.nix` `perSystem.packages` — `phillipg-nix-repo-base-marketplace = (mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace { src = lib.fileset.toSource { root = ./.; fileset = lib.fileset.unions [ ./.claude-plugin/marketplace.json ./pn-workspace-rules ]; }; };` (identity `phillipg-nix-repo-base-marketplace-local`). Optionally surface via `overlays.default`.
5. `.claude-plugin/marketplace.json` — `name`: `pn-workspace` → `phillipg-nix-repo-base`.
6. `pn-workspace-rules/.claude-plugin/plugin.json` — add `"defaultEnabled": true` (rules plugin, always-on; also correct for direct-install).
7. New ADR superseding/amending ADR-0003 (identity convention `<repo>-marketplace[-local]`; mkClaudeMarketplace fulfils "Phase 4 auto-install"). Update CLAUDE.md/skills/docs references to the old `pn-workspace` marketplace identity.

## agent-support changes

1. New `home/programs/claude-marketplaces` module exposing the consumer-control surface (mirrors the existing `plugins.local.overrides` / `plugins.thirdparty.overrides` patterns):
   - `phillipgreenii.programs.claude.marketplaces.nixProvided = [ <marketplace drvs> ]` — the registered marketplaces (auto-populated for repo-base's by `homeModules.default`).
   - `…marketplaces.enabled = { "<marketplaceName>" = bool; }` (default true) — **per-marketplace toggle**; when false the module emits NOTHING for it (no extraKnownMarketplaces / enabledPlugins / plugins / symlink), so a consumer (e.g. ziprecruiter) can opt out of an upstream-provided marketplace.
   - `…marketplaces.overrides = { "<plugin>@<marketplaceName>" = bool; }` — **per-plugin** enable override (resolution: override → plugin `defaultEnabled` → false). Passed as the `enabled` arg to `mkDirectoryMarketplaceSettings`.
     For each `m` where `marketplaces.enabled.${m.marketplaceName} != false`:
   - `home.file."<root>/${m.marketplaceName}".source = m;` (symlink the built marketplace into a fixed path, e.g. `~/.local/share/pgii-marketplaces/<marketplaceName>`);
   - merge `mkDirectoryMarketplaceSettings { marketplace = m; path = "<homeDir>/<root>/${m.marketplaceName}"; enabled = cfg.marketplaces.overrides; }` into `phillipgreenii.programs.claude.settings`.
     Settings option types confirmed mergeable across modules: `extraKnownMarketplaces` attrsOf(attrsOf anything), `enabledPlugins` attrsOf bool, `plugins` listOf str — coexists with pgii-local-plugins/pgii-claude-plugins.
2. **Thread the marketplace drv via an option set in `homeModules.default`** (flake-level, where `inputs` is in scope — like `pluginVersion` at flake.nix:896 / the `install-metadata` wrapper), NOT by reaching into `inputs` inside the program module (agent-support sets no `extraSpecialArgs`; the `serena` precedent only works because the consumer machine flake passes `inputs`).
3. **System-set guard:** repo-base publishes only `x86_64-linux` + `aarch64-darwin`; agent-support builds 4 systems. Register the marketplace only on systems where `inputs.phillipgreenii-nix-base.packages.${system}` has it (or expose it system-independently), else the extra systems eval-error.
4. Import the module in `home/default.nix`.

## Enable/disable

- Per-plugin default = `plugin.json` `defaultEnabled` (absent ⇒ `false`).
- Consumer override via `enabled = { "<plugin>@<mkt>" = bool; }` (or bare `<plugin>`).
- `enabledPlugins` carries the resolved bool; `plugins` lists all (registered/known) so toggling needs no reinstall.
- `pn-workspace-rules` gets `defaultEnabled: true`.

## Migration / coexistence

- B4 (follow-up): rebuild agent-support's `pgii-local-plugins` aggregate via `mkClaudeMarketplace` as `phillipgreenii-nix-agent-support-marketplace-local`, consolidating its local plugins (6 always-registered: agent-rules, bash-lsp, bash-scripting, bead-grooming, claude-activity, claude-extended-tool-approver; plus pg-pr and ccpool when their feature modules are enabled — verify the exact set at B4 time) and deleting duplicated per-module version-stamping. Rename ⇒ one-time re-enable churn (precedented by agent-support ADR-0003). **Acceptance criteria MUST include:** (a) every migrated plugin.json gets `defaultEnabled: true` — else the new default-`false` polarity silently disables all 8; (b) handle the dual version scheme during transition (`<declared>+<digest>` vs the shared `self.lib.pluginVersion` `0.YYYY.MMDDHHMMSS`, which is also load-bearing at flake.nix:896 — remove with care).
- External marketplaces (beads, superpowers, claude-plugins-official) stay registered via the existing `pgii-claude-plugins` thirdparty (github) path. Unaffected.

## Stragglers (workspace sweep result)

- No true Claude-plugin stragglers: only repo-base and agent-support ship Claude plugin content; all maps to per-repo marketplaces.
- Not stragglers (separate, Gas-City-legacy mechanisms; flag for separate cleanup): per-repo `.claude/skills/` symlinks → `/Users/phillipg/gc/.gc/system/packs/core/skills/*` (project-scoped, likely dead since gc decommissioned 2026-06-11); `pgii-pack-*` → gascity `pack.toml`/`city.toml` imports (gc agents, not Claude plugins).

## Documentation deliverable (ships with the new modules)

A how-to doc must accompany the new modules describing the **two patterns** for getting a plugin to agents. Primary copy: `phillipg-nix-repo-base/docs/claude-marketplaces.md` (the builder is the convention origin); referenced from `lib/claude-marketplace.nix` (header comment) and from `phillipgreenii-nix-agent-support/home/programs/claude-marketplaces/` (module comment / short README pointing at the repo-base doc). The new ADR records the _decision_; this doc is the _usage guide_. Must cover:

### Pattern 1 — Produce a marketplace (in any nix-\* repo)

1. Lay out a Claude marketplace per the spec at the repo root: `.claude-plugin/marketplace.json` (`name` = the repo name) + one dir per plugin, each with `.claude-plugin/plugin.json`. Set `"defaultEnabled": true` in a plugin's manifest to have it on by default (absent ⇒ off).
2. Build + expose it: in `perSystem.packages`, `<repo>-marketplace = (mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace { src = lib.fileset.toSource { root = ./.; fileset = …(.claude-plugin + plugin dirs)…; }; };`. Result: a store artifact named `<repo>-marketplace-local` with each plugin version stamped `<declared>+<digest>` (content-derived; auto-busts the Claude cache on change) and `passthru.{marketplaceName, plugins}`.
3. The source `marketplace.json` keeps the bare repo name, so `claude plugin marketplace add <repo>` still works for a direct/manual install (distinct from the `-local` nix variant).

### Pattern 2 — Register + control it (consumer: agent-support / a machine flake)

1. Register: add the marketplace drv to `phillipgreenii.programs.claude.marketplaces.nixProvided` (repo-base's is auto-added by agent-support's `homeModules.default`; other repos' are added explicitly).
2. Per-marketplace toggle: `marketplaces.enabled."<repo>-marketplace-local" = false;` fully unregisters it (no settings keys, no symlink).
3. Per-plugin enable/disable: `marketplaces.overrides."<plugin>@<repo>-marketplace-local" = false|true;` (resolution: override → plugin `defaultEnabled` → false).
4. What it writes: `extraKnownMarketplaces` (directory source) + `enabledPlugins` + `plugins`, via agent-support's single `claude-settings` writer; on-disk a symlink under `~/.local/share/pgii-marketplaces/` that Claude copies into its versioned cache.
   Include a worked `phillipg-nix-ziprecruiter` example (the `home/ziprecruiter/packages/default.nix` block) showing both an override and a per-marketplace disable.

## Work breakdown (beads)

- B1 (repo-base): `lib/claude-marketplace.nix` (mkClaudePlugin + mkClaudeMarketplace + mkDirectoryMarketplaceSettings, `{pkgs,lib}` only) + tests + lib export. **Validated sound; ready.**
- B2 (repo-base): wire `phillipg-nix-repo-base-marketplace` package; set `defaultEnabled: true`; rename root marketplace.json `name`; new ADR; update references.
- B3 (agent-support, re-scopes `pg2-7j5j`): `marketplaces.nixProvided` module (drv threaded via homeModules.default, system-guarded) + import + register repo-base marketplace.
- B4 (follow-up): migrate `pgii-local-plugins` → `mkClaudeMarketplace`; second construction mode spec; the two acceptance-criteria hazards above.
- B5 (follow-up): purge dead Gas City `.claude/skills` symlinks + `pgii-packs` wiring.
- B6 (docs — a sub-deliverable of B2+B3, NOT a deferral; ships with them): `phillipg-nix-repo-base/docs/claude-marketplaces.md` covering both patterns (produce + register/control), with the worked ziprecruiter example; pointers from `lib/claude-marketplace.nix` and the agent-support `claude-marketplaces` module. State once, authoritatively, that the version's on-disk/manifest form is `<declared>+<digest>` and the `+` is sanitized to `-` only in the Claude cache _path_ (so implementers don't second-guess the literal `+`). Distinct from the ADR (decision) — this is the usage guide.

Implementation order: **B1** (pure builder + tests, no consumers) → **B2 then/with B3** (B3's registration of the repo-base marketplace depends on B2's renamed manifest `name` + `defaultEnabled`), with **B6** riding alongside B2/B3. **B4, B5** are the only true deferrals.

- Decision record (bd): federation (per-repo marketplace) over aggregation; version owned by source repo via content digest; single settings writer in agent-support; naming `<repo>-marketplace[-local]`; gate result (`+digest` busts cache, verified 2.1.186).
