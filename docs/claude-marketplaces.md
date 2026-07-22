# Claude marketplaces: producing, registering, and controlling plugins

How to deliver Claude Code plugins to agents declaratively via nix. There are two
patterns: **producing** a marketplace (any `nix-*` repo) and **registering +
controlling** it (a consumer — `phillipgreenii-nix-agent-support` or a machine
flake).

The builder is the convention origin: `lib/claude-marketplace.nix` in this repo
(`phillipg-nix-repo-base`). The decision record is
[ADR-0010](adr/0010-claude-marketplace-builder-and-identity.md); this is the usage
guide.

## Version form (read this once)

A plugin's version is stamped to **`<declared>+<digest>`**, where `<declared>` is the
plugin's own `plugin.json` version and `<digest>` is an 8-char content digest of the
plugin's own subtree (`mkSrcDigest (builtins.path { … })`). The version is
**content-derived** — it changes iff that plugin's content changes, never on the repo
git rev (see [ADR-0006](adr/0006-source-content-digest-versioning.md)) and never on
unrelated repo edits.

The literal `+` is correct in the manifest and on disk. Claude sanitizes it to `-`
**only inside the cache path** (`~/.claude/plugins/cache/<mkt>/<plugin>/<version>/`,
e.g. `1.0.0-abc12345/`). A digest change is recognized as a version change and busts
the cache (verified on Claude Code 2.1.186). Do not second-guess the literal `+` in
the manifest or invent a `-<digest>` fallback.

## Pattern 1 — Produce a marketplace (in any nix-\* repo)

1. **Lay out a Claude marketplace at the repo root** per the spec:
   - `.claude-plugin/marketplace.json` — its `name` is the **repo name** (bare, no
     suffix). Lists the plugins with their `path`s.
   - one dir per plugin, each with `.claude-plugin/plugin.json` + content.
   - Set `"defaultEnabled": true` in a plugin's `plugin.json` to have it on by
     default. **Absent ⇒ off** — so every plugin you want loaded must declare it.

   **Plugin content must be a skill/agent/hook — NOT a root `CLAUDE.md`.**
   Verified on Claude Code 2.1.186: a plugin-root `CLAUDE.md` is **not** injected
   into agent context, and `plugin.json` `type`/`content` fields are silently
   ignored (`claude plugin validate` flags them as Unknown). The marketplace still
   registers and enables the plugin, but the `CLAUDE.md`/`type: rules` convention
   from ADR-0003 does nothing.

   **Pick the delivery vehicle by when the content must apply:**
   - **Always-on rules → a SessionStart HOOK plugin.** Ship
     `<plugin>/hooks/hooks.json` declaring a `SessionStart` hook whose command
     prints the rules to stdout as `additionalContext`
     (`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}`,
     exit 0). The hook fires on **every** session — interactive and headless
     `claude -p` alike — so the rules are unconditionally in context. This is the
     only plugin vehicle that is genuinely always-on: a skill body is on-invoke,
     and a plugin-root `CLAUDE.md` is inert. Reference the hook command by **bare
     name** (resolved on `PATH`), not an absolute store path; the plugin's
     home-manager module installs that binary on `PATH`. Example: agent-support's
     `agent-rules` plugin (`agent-rules/hooks/hooks.json` → `agent-rules-session-start`).
   - **Context-specific / on-invoke rules → a SKILL.** Ship
     `<plugin>/skills/<name>/SKILL.md` with `name` + `description` frontmatter; the
     body loads only when its `description` triggers, so write a strong triggering
     description. This is correct when the rules apply to a specific context rather
     than every turn — e.g. `pn-workspace-rules` ships
     `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`.
   - **Dispatched sub-task work → an AGENT.** Ship `<plugin>/agents/<name>.md`
     with `name` + `description` (plus optional `tools`/`model`) frontmatter; a
     command or the main session dispatches it via the Task tool to run a
     self-contained sub-task in its own isolated context. Correct when the work is
     worth offloading from the caller's context — not rules applied in-line — e.g.
     `pn-workspace-rules` ships `pn-workspace-rules/agents/pnwf-runner.md`,
     dispatched by `/pn-workspace-sync` to run its fork/sync/validate prefix.

   Proof: `claude plugin details <plugin>@<mkt>` shows `Skills (1)` and a nonzero
   always-on token cost for a skill-shipping plugin vs `Skills (0)` / ~0 tokens for
   a root-`CLAUDE.md` plugin.

2. **Build + expose it** in `perSystem.packages`:

   ```nix
   <repo>-marketplace =
     (mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace {
       src = lib.fileset.toSource {
         root = ./.;
         fileset = lib.fileset.unions [
           ./.claude-plugin/marketplace.json
           ./<plugin-dir>            # one per plugin
         ];
       };
     };
   ```

   `mkClaudeMarketplaceBuilders` is exposed un-applied on
   `phillipg-nix-repo-base`'s `flake.lib` (apply it with your own `{ pkgs, lib }`,
   exactly like `mkBashBuilders` / `mkGoBuilders`).

   **Use a narrowed `lib.fileset.toSource`, NOT `./.`.** Passing the whole repo
   realizes the entire tree into the store (closure bloat: `.git`, docs, Go trees)
   and makes the artifact's drvPath depend on every unrelated repo edit, re-realizing
   it even when per-plugin content is unchanged.

   Result: a store artifact whose regenerated `marketplace.json` `name` is
   **`<repo>-marketplace-local`** with each plugin version stamped `<declared>+<digest>`,
   carrying:

   ```nix
   passthru = {
     marketplaceName = "<repo>-marketplace-local";
     plugins = [ { name; version; key = "<name>@<repo>-marketplace-local"; defaultEnabled; } … ];
   };
   ```

3. **The source `marketplace.json` keeps the bare repo name**, so a direct/manual
   install via `claude plugin marketplace add <repo>` still works — distinct from
   (and non-colliding with) the `-local` nix variant.

This repo's own example (`flake.nix`):

```nix
phillipg-nix-repo-base-marketplace =
  (mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace {
    src = lib.fileset.toSource {
      root = ./.;
      fileset = lib.fileset.unions [
        ./.claude-plugin/marketplace.json
        ./pn-workspace-rules
      ];
    };
  };
```

Build + inspect it:

```bash
nix build .#phillipg-nix-repo-base-marketplace
cat result/.claude-plugin/marketplace.json          # name = …-marketplace-local
cat result/pn-workspace-rules/.claude-plugin/plugin.json  # version = 1.0.0+<digest>
ls result/pn-workspace-rules/skills/pn-workspace-rules/SKILL.md  # the loaded content
claude plugin validate ./result
```

## Pattern 2 — Register + control it (consumer: agent-support / a machine flake)

Registration is owned by `phillipgreenii-nix-agent-support`'s single
`claude-settings` writer (it owns `~/.claude/settings.json`). The repo-base builder
only **produces** the artifact; the agent-support `claude-marketplaces` module
**registers** it. (That consumer module lives in a separate repo and is out of scope
for this doc; the surface below is the contract.)

1. **Register** — add the marketplace drv to
   `phillipgreenii.programs.claude.marketplaces.nixProvided`. repo-base's is
   auto-added by agent-support's `homeModules.default`; other repos' are added
   explicitly.

2. **Per-marketplace toggle** — fully unregister a marketplace (no settings keys, no
   symlink):

   ```nix
   phillipgreenii.programs.claude.marketplaces.enabled."<repo>-marketplace-local" = false;
   ```

3. **Per-plugin enable/disable** — override an individual plugin:

   ```nix
   phillipgreenii.programs.claude.marketplaces.overrides."<plugin>@<repo>-marketplace-local" = false;  # or true
   ```

   Resolution per plugin: **override → plugin `defaultEnabled` → false**. Overrides
   accept either the `"<plugin>@<mkt>"` key form or the bare `"<plugin>"` name.

4. **What it writes** — via the single `claude-settings` writer:
   `extraKnownMarketplaces` (a `directory` source pointing at an on-disk symlink under
   `~/.local/share/pgii-marketplaces/<marketplaceName>`), `enabledPlugins` (resolved
   bool per plugin), and `plugins` (all keys, so toggling needs no reinstall). Claude
   copies the directory source into its versioned cache; the `<declared>+<digest>`
   version busts that cache on content change.

### Worked example — `phillipg-nix-ziprecruiter`

A machine/consumer flake (e.g. the `home/ziprecruiter/packages/default.nix` block)
showing both a per-plugin override and a per-marketplace disable:

```nix
phillipgreenii.programs.claude.marketplaces = {
  # Disable an upstream-provided marketplace entirely for this consumer.
  enabled."some-other-repo-marketplace-local" = false;

  # Keep repo-base's marketplace, but override one plugin's enable state.
  overrides = {
    "pn-workspace-rules@phillipg-nix-repo-base-marketplace-local" = true;  # explicit on
    # "noisy-plugin@phillipg-nix-repo-base-marketplace-local" = false;     # explicit off
  };
};
```

## Pure helper: `mkDirectoryMarketplaceSettings`

If you are not going through the agent-support module, `mkClaudeMarketplaceBuilders`
also returns the pure `mkDirectoryMarketplaceSettings { marketplace, path, enabled ? {} }`
that produces the settings fragment directly. The agent-support module is built on top
of it.

## Where things live

- Builder + this convention: `lib/claude-marketplace.nix`,
  `lib/claude-marketplace-tests.nix` (this repo).
- Decision record: [ADR-0010](adr/0010-claude-marketplace-builder-and-identity.md)
  (amends [ADR-0003](adr/0003-claude-marketplace-convention.md)).
- The consumer-control module (`marketplaces.{nixProvided,enabled,overrides}`) lives
  in `phillipgreenii-nix-agent-support` (separate repo — out of scope here).
