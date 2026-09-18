# mkClaudeMarketplaceBuilders — factory for Claude Code marketplace packaging builders
#
# Takes { pkgs, lib } and returns
#   { mkClaudePlugin, mkClaudeMarketplace, mkDirectoryMarketplaceSettings,
#     mkClaudeHookRouterPlugin }.
#
# NOTE: this factory deliberately takes NO `self`. Marketplace/plugin versions are
# derived from a per-source CONTENT digest (see mkSrcDigest below), never from the
# repo git rev — threading `self` would invite reintroducing the `self.rev` churn
# that ADR-0006 exists to kill.
#
# Usage convention + the two delivery patterns (produce vs. register/control) are
# documented in docs/claude-marketplaces.md. The on-disk/manifest version form is
# `<declared>+<digest>`; the `+` is sanitized to `-` only inside Claude's cache PATH
# (see that doc and ADR-0010).
{
  pkgs,
  lib,
}:
let
  versionLib = import ./version.nix;

  # Stamp a plugin's own manifest version to `<declared>+<digest>`.
  #
  # mkSrcDigest hashes the STORE-PATH STRING of its argument (see version.nix
  # NOTE — it is NOT a NAR content hash). We wrap `src` in `builtins.path` so the
  # store path is CONTENT-ADDRESSED: the digest changes iff THIS plugin's content
  # changes, and is stable for unrelated repo edits (no IFD — reads source, never
  # built output). Do NOT "fix" this to hash raw file content; the content-address
  # of the scoped subtree is precisely the property we want.
  stampVersion =
    {
      name,
      src,
      declared,
    }:
    let
      digest = versionLib.mkSrcDigest (
        builtins.path {
          path = src;
          name = "${name}-src";
        }
      );
    in
    "${declared}+${digest}";

  # mkClaudePlugin — bundle a single plugin directory into the store, stamping its
  # plugin.json version to `<declared>+<digest>`.
  #
  # Arguments:
  #   src — path to the plugin directory (contains .claude-plugin/plugin.json + content)
  #
  # Returns: a derivation whose $out is the plugin dir with plugin.json version
  # overwritten to the stamped value, carrying
  #   passthru = { pluginName; version; defaultEnabled; }.
  mkClaudePlugin =
    { src }:
    let
      manifest = builtins.fromJSON (builtins.readFile (src + "/.claude-plugin/plugin.json"));
      pluginName = manifest.name;
      declared = manifest.version;
      defaultEnabled = manifest.defaultEnabled or false;
      version = stampVersion {
        name = pluginName;
        inherit src declared;
      };
      drv =
        pkgs.runCommand "claude-plugin-${pluginName}"
          {
            nativeBuildInputs = [ pkgs.jq ];
            passthru = {
              inherit pluginName version defaultEnabled;
            };
          }
          ''
            mkdir -p "$out"
            cp -r ${src}/. "$out/"
            chmod -R u+w "$out"
            jq --arg v ${lib.escapeShellArg version} '.version = $v' \
              "$out/.claude-plugin/plugin.json" > "$out/.claude-plugin/plugin.json.tmp"
            mv "$out/.claude-plugin/plugin.json.tmp" "$out/.claude-plugin/plugin.json"
          '';
    in
    drv;

  # mkClaudeMarketplace — bundle an in-repo Claude marketplace into the store with
  # content-derived per-plugin version stamping.
  #
  # `src` MUST be a narrowed `lib.fileset.toSource` (the .claude-plugin dir + the
  # listed plugin dirs), NOT `./.` — passing the whole repo realizes the entire
  # tree into the store (closure bloat) and makes the artifact's drvPath depend on
  # every unrelated repo edit. See ADR-0008 / ADR-0010 and docs/claude-marketplaces.md.
  #
  # Arguments:
  #   src        — narrowed source root containing .claude-plugin/marketplace.json
  #                and the per-plugin dirs it lists
  #   nameSuffix — appended to the repo name to form the installed identity
  #                (default "-local"; marks the nix-built variant)
  #
  # Returns: a derivation whose $out is the regenerated marketplace (suffixed name +
  # stamped per-plugin versions) with each plugin dir copied and its plugin.json
  # version overwritten. Carries
  #   passthru = {
  #     marketplaceName;
  #     plugins = [ { name; version; key = "<name>@<marketplaceName>"; defaultEnabled; } … ];
  #   }.
  # The marketplace derivation itself carries NO repo-rev-dependent version.
  mkClaudeMarketplace =
    {
      src,
      nameSuffix ? "-local",
    }:
    let
      manifest = builtins.fromJSON (builtins.readFile (src + "/.claude-plugin/marketplace.json"));
      marketplaceName = "${manifest.name}-marketplace${nameSuffix}";

      # A local plugin entry locates its dir via `source` — a relative path string,
      # conventionally "./<dir>" (Claude Code marketplace schema). Strip a leading
      # "./" to get the store-relative subpath we copy from / write to. (Non-string
      # `source` forms — e.g. a remote url object — are not supported by this
      # directory builder.)
      relPath = entry: lib.removePrefix "./" entry.source;

      # Per-plugin metadata: read the plugin's own manifest, scope a per-plugin
      # `builtins.path` digest, resolve defaultEnabled (absent ⇒ false).
      pluginInfos = map (
        entry:
        let
          path = relPath entry;
          pluginSrc = src + "/${path}";
          pluginManifest = builtins.fromJSON (builtins.readFile (pluginSrc + "/.claude-plugin/plugin.json"));
          inherit (pluginManifest) name;
          version = stampVersion {
            inherit name;
            src = pluginSrc;
            declared = pluginManifest.version;
          };
        in
        {
          inherit name version path;
          inherit (entry) source;
          defaultEnabled = pluginManifest.defaultEnabled or false;
        }
      ) manifest.plugins;

      # Regenerated marketplace.json: suffixed name + per-plugin stamped versions.
      # We carry the stamped version onto each marketplace plugin entry as well so
      # consumers reading the listing (rather than the plugin.json) agree.
      regeneratedManifest = manifest // {
        name = marketplaceName;
        plugins = map (
          entry:
          let
            info = lib.findFirst (p: p.source == entry.source) (throw "plugin info not found") pluginInfos;
          in
          entry // { inherit (info) version; }
        ) manifest.plugins;
      };
      manifestFile = pkgs.writeText "marketplace.json" (builtins.toJSON regeneratedManifest);

      # Per-plugin copy + version-stamp steps. Each plugin dir is copied to the
      # store-relative path its `source` names and its plugin.json version overwritten.
      copySteps = lib.concatMapStringsSep "\n" (info: ''
        mkdir -p "$out/$(dirname ${lib.escapeShellArg info.path})"
        cp -r ${src}/${info.path} "$out/${info.path}"
        chmod -R u+w "$out/${info.path}"
        jq --arg v ${lib.escapeShellArg info.version} '.version = $v' \
          "$out/${info.path}/.claude-plugin/plugin.json" > "$out/${info.path}/.claude-plugin/plugin.json.tmp"
        mv "$out/${info.path}/.claude-plugin/plugin.json.tmp" "$out/${info.path}/.claude-plugin/plugin.json"
      '') pluginInfos;
    in
    pkgs.runCommand "claude-marketplace-${manifest.name}"
      {
        nativeBuildInputs = [ pkgs.jq ];
        passthru = {
          inherit marketplaceName;
          plugins = map (info: {
            inherit (info) name version defaultEnabled;
            key = "${info.name}@${marketplaceName}";
          }) pluginInfos;
        };
      }
      ''
        mkdir -p "$out/.claude-plugin"
        cp ${manifestFile} "$out/.claude-plugin/marketplace.json"
        ${copySteps}
      '';

  # mkDirectoryMarketplaceSettings — pure helper producing the declarative
  # `~/.claude/settings.json` fragment that registers a built marketplace as a
  # local directory source.
  #
  # Arguments:
  #   marketplace — a mkClaudeMarketplace result (its passthru carries identity)
  #   path        — the on-disk install path Claude reads the directory source from
  #   enabled     — optional per-plugin overrides keyed by "<name>@<mkt>" or "<name>"
  #
  # Resolution per plugin: enabled.<key> → enabled.<name> → plugin defaultEnabled.
  mkDirectoryMarketplaceSettings =
    {
      marketplace,
      path,
      enabled ? { },
    }:
    {
      extraKnownMarketplaces.${marketplace.marketplaceName}.source = {
        source = "directory";
        inherit path;
      };
      enabledPlugins = lib.listToAttrs (
        map (
          p: lib.nameValuePair p.key (enabled.${p.key} or enabled.${p.name} or p.defaultEnabled)
        ) marketplace.plugins
      );
      plugins = map (p: p.key) marketplace.plugins;
    };

  # mkClaudeHookRouterPlugin — merge several source plugins' surfaces (hooks,
  # commands, agents, skills, MCP servers) into ONE generated plugin directory
  # that vendors each source, namespaced to avoid cross-source collisions, with
  # every hook delegated to a single router binary. See `docs/adr/0071-...`
  # (`phillipgreenii-nix-agent-support`) §2.6 for the full design; this is the
  # nix-side generator only (A1) — the Go runtime that reads `router-config.json`
  # is a separate package (Phase B).
  #
  # Arguments:
  #   name        — the generated plugin's own name (`.claude-plugin/plugin.json`)
  #   declared    — the generated plugin's own declared version (stamped to
  #                 `<declared>+<digest>` like every other builder in this file;
  #                 the digest covers every source's content)
  #   description — optional, generated plugin.json `description` (default "")
  #   routerCommand — bare command name every generated `hooks.json` entry
  #                 points at (default "claude-hook-router"; the real Phase B
  #                 binary name is TBD, so this is overridable per ADR 0071 §2.7)
  #   sources     — list of `{ name; src; includeHooks ? false; includeCommands
  #                 ? false; includeAgents ? false; includeSkills ? false;
  #                 includeMcp ? false; priority; }`. `priority` is required for
  #                 any source with `includeHooks = true` (a per-source banded
  #                 ordering value the caller supplies — computing it is Phase
  #                 C's concern; this builder only carries it through unchanged
  #                 into every `router-config.json` tuple that source
  #                 contributes). Source `name`s must be unique.
  #
  # Per-source processing (§2.6):
  #   - includeCommands/includeAgents: an explicit `commands`/`agents` array in
  #     the source's own plugin.json REPLACES its default directory scan.
  #   - includeSkills: an explicit `skills` array ADDS TO the default directory
  #     scan (both are copied).
  #   - Every copied surface is namespaced under `$out/<surface>/<name>/…`
  #     (nesting, never renaming — the A1a empirical finding: Claude Code
  #     disambiguates same-named commands/agents by subdirectory path).
  #   - includeMcp: that source's `mcpServers` (from `.mcp.json` and/or an
  #     inline `plugin.json` `mcpServers` key) are merged into `$out/.mcp.json`,
  #     each key prefixed `<name>-`. A collision even after prefixing is a
  #     build-time error (distinct source names can still collide, e.g.
  #     "foo"+"bar-baz" vs "foo-bar"+"baz").
  #   - includeHooks: every event in that source's `hooks/hooks.json` (or an
  #     inline `plugin.json` `hooks` key) is parsed — not just
  #     `PreToolUse`/`Bash` — and a single matcher-group's `hooks` array (which
  #     may hold several `{type:"command",...}` entries) expands into that many
  #     separate `router-config.json` delegates. Each source hook entry MUST
  #     carry an extension `contract` field (one of `decide`/`rewrite`/
  #     `annotate`/`observe`/`decide+rewrite`) valid for its event (§2.4's
  #     table below) — a mismatch, or a missing `contract`, is a build-time
  #     error, never a silent no-op. `${CLAUDE_PLUGIN_ROOT}` occurrences in the
  #     command (zero, one, or many) are rewritten to
  #     `${CLAUDE_PLUGIN_ROOT}/vendored/<name>` (the source's post-merge path).
  #
  # Per-event registration (§2.3/§2.6, after all sources are processed): one
  # `hooks.json` entry per event with ≥1 delegate, using the broadest matcher
  # needed — match-all if any delegate wants match-all, the shared matcher if
  # every delegate on that event agrees, otherwise match-all as the chosen
  # fallback policy for genuinely conflicting non-match-all matchers (the
  # simpler, always-correct option from the two the ADR leaves as an explicit,
  # undecided choice — the router's own dispatch loop still filters per
  # delegate at runtime using each delegate's ORIGINAL matcher from
  # `router-config.json`, so this registration-level union is only ever a
  # coarser pre-filter, never a correctness issue). An event with zero
  # delegates gets no `hooks.json` entry at all. A vendored delegate's matcher
  # outside the exact-string/pipe-alternation subset (§2.3's classification —
  # anything else is regex-shaped) is rejected at build time for manual
  # review, rather than trusting Go-RE2/JS-regex parity silently.
  #
  # Output ($out): `.claude-plugin/plugin.json` (version-stamped), a
  # `hooks/hooks.json` with one entry per event that has a delegate — every
  # entry pointing at `routerCommand` — `router-config.json` (delegates grouped
  # by event, each event's list sorted priority-ascending then name), the
  # merged `commands`/`agents`/`skills` trees, and `.mcp.json`. No symlink
  # anywhere under $out may resolve outside $out (Claude Code's own
  # directory-source cache-copy step silently drops such symlinks) — every
  # copy dereferences symlinks (`cp -r --dereference`), and the build asserts
  # none remain.
  mkClaudeHookRouterPlugin =
    {
      name,
      declared,
      sources,
      description ? "",
      routerCommand ? "claude-hook-router",
    }:
    let
      # Elements of `list` that occur more than once (order-preserving,
      # de-duplicated). `lib.subtractLists` is NOT this — it drops every
      # occurrence of a shared element from both sides, so it always returns
      # `[ ]` here regardless of duplicates.
      duplicatesIn = list: lib.filter (x: lib.count (y: y == x) list > 1) (lib.unique list);

      # §2.4's event -> allowed-contract table (consolidating the prose into
      # one lookup the generator's reject-mismatched-contract obligation can
      # check against). An event absent from this table (e.g. PostToolUseFailure,
      # SessionStart — named by §2.3 as matcher-filterable but not enumerated by
      # §2.4's contract list) defaults to observe-only, the design's own stated
      # conservative default for the unenumerated case.
      allowedContractsByEvent = {
        PreToolUse = [
          "decide"
          "rewrite"
          "annotate"
          "observe"
          "decide+rewrite"
        ];
        PermissionRequest = [
          "decide"
          "rewrite"
          "annotate"
          "observe"
          "decide+rewrite"
        ];
        PostToolUse = [
          "rewrite"
          "annotate"
          "observe"
        ];
        PermissionDenied = [
          "decide"
          "annotate"
          "observe"
        ];
        SessionEnd = [ "observe" ];
      };
      contractsForEvent = event: allowedContractsByEvent.${event} or [ "observe" ];

      # §2.3 matcher classification: only letters/digits/_/-/spaces/,/| is
      # exact-string-or-pipe-alternation; absent/"*"/"" is match-all; anything
      # else is regex-shaped.
      classifyMatcher =
        m:
        if m == null || m == "" || m == "*" then
          "match-all"
        else if builtins.match "[A-Za-z0-9_, |-]+" m != null then
          "exact"
        else
          "regex";

      normalizeSource = s: rec {
        inherit (s) name src;
        includeHooks = s.includeHooks or false;
        includeCommands = s.includeCommands or false;
        includeAgents = s.includeAgents or false;
        includeSkills = s.includeSkills or false;
        includeMcp = s.includeMcp or false;
        # Only forced for a source that actually contributes hook delegates
        # (nix laziness: this thunk is never touched for includeHooks = false).
        priority =
          s.priority
            or (throw "mkClaudeHookRouterPlugin: source '${s.name}' has includeHooks = true and must set priority");
        manifest = builtins.fromJSON (builtins.readFile (src + "/.claude-plugin/plugin.json"));
      };

      rawNames = map (s: s.name) sources;
      dupNames = duplicatesIn rawNames;
      normSources =
        if dupNames != [ ] then
          throw "mkClaudeHookRouterPlugin: duplicate source name(s): ${lib.concatStringsSep ", " dupNames}"
        else
          map normalizeSource sources;

      # --- hooks / router-config.json ---

      rewriteCommand =
        s: cmd:
        builtins.replaceStrings [ "\${CLAUDE_PLUGIN_ROOT}" ] [ "\${CLAUDE_PLUGIN_ROOT}/vendored/${s.name}" ]
          cmd;

      readSourceDelegates =
        s:
        let
          hooksJsonPath = s.src + "/hooks/hooks.json";
          hasHooksJson = builtins.pathExists hooksJsonPath;
          hooksDoc =
            if hasHooksJson then
              builtins.fromJSON (builtins.readFile hooksJsonPath)
            else if s.manifest ? hooks then
              { hooks = s.manifest.hooks; }
            else
              throw "mkClaudeHookRouterPlugin: source '${s.name}' has includeHooks = true but neither hooks/hooks.json nor an inline plugin.json 'hooks' key was found";
          eventsMap = hooksDoc.hooks or { };
          delegatesForGroup =
            event: group:
            let
              matcher = group.matcher or null;
            in
            map (
              hookEntry:
              let
                contract =
                  hookEntry.contract
                    or (throw "mkClaudeHookRouterPlugin: source '${s.name}' event '${event}' hook entry is missing the required 'contract' extension field");
              in
              if (hookEntry.type or null) != "command" then
                throw "mkClaudeHookRouterPlugin: source '${s.name}' event '${event}' has a hook entry whose type is not 'command'"
              else if !(lib.elem contract (contractsForEvent event)) then
                throw "mkClaudeHookRouterPlugin: source '${s.name}' event '${event}' declares contract '${contract}', which is not supported by that event (allowed: ${lib.concatStringsSep ", " (contractsForEvent event)})"
              else if classifyMatcher matcher == "regex" then
                throw "mkClaudeHookRouterPlugin: source '${s.name}' event '${event}' matcher '${matcher}' is regex-shaped — only exact-string/pipe-alternation matchers are accepted without manual review"
              else
                {
                  inherit (s) name priority;
                  inherit event;
                  matcher = if classifyMatcher matcher == "match-all" then null else matcher;
                  command = rewriteCommand s hookEntry.command;
                  inherit contract;
                }
            ) group.hooks;
          delegatesForEvent = event: groups: lib.concatMap (delegatesForGroup event) groups;
        in
        lib.concatLists (lib.mapAttrsToList delegatesForEvent eventsMap);

      allDelegates = lib.concatMap (s: if s.includeHooks then readSourceDelegates s else [ ]) normSources;
      delegatesByEvent = lib.groupBy (d: d.event) allDelegates;

      # Broadest matcher needed per event: match-all if any delegate wants it,
      # the shared matcher if every delegate on this event agrees, otherwise
      # match-all as the chosen fallback (see the doc comment above).
      unionMatcherForEvent =
        delegates:
        let
          matchers = map (d: d.matcher) delegates;
        in
        if lib.any (m: m == null) matchers then
          null
        else if lib.all (m: m == builtins.head matchers) matchers then
          builtins.head matchers
        else
          null;

      # Per event, the value under `hooks.<Event>` is itself an ARRAY of
      # matcher-groups (Claude Code's own `hooks.json` shape — see e.g.
      # claude-extended-tool-approver's real hooks.json, cited by the design
      # at §1.3): `[ { matcher?; hooks: [ {type;command} ] } ]`. This builder
      # only ever emits one group per event (the union matcher above), but the
      # VALUE must still be that one-element array, not an object wrapping it.
      hooksJsonEvents = lib.mapAttrs (
        _event: delegates:
        let
          m = unionMatcherForEvent delegates;
          hookCmd = {
            type = "command";
            command = routerCommand;
          };
        in
        [ ({ hooks = [ hookCmd ]; } // (if m == null then { } else { matcher = m; })) ]
      ) delegatesByEvent;

      sortDelegates = lib.sort (
        a: b: if a.priority != b.priority then a.priority < b.priority else a.name < b.name
      );
      routerConfig = lib.mapAttrs (_event: sortDelegates) delegatesByEvent;

      # --- commands / agents / skills ---

      surfaces = [
        {
          dir = "commands";
          flag = "includeCommands";
          additive = false;
        }
        {
          dir = "agents";
          flag = "includeAgents";
          additive = false;
        }
        {
          dir = "skills";
          flag = "includeSkills";
          additive = true;
        }
      ];

      # Items to copy for one (source, surface): the whole default-scan
      # directory (represented by the surface name itself) and/or the
      # source's own explicitly declared relative paths, per §2.6's
      # replace-vs-add-to resolution semantics.
      surfaceCopyItems =
        s: surf:
        let
          declared = s.manifest.${surf.dir} or null;
          defaultDir = s.src + "/${surf.dir}";
          hasDefault = builtins.pathExists defaultDir;
          declaredItems = if declared == null then [ ] else map (p: lib.removePrefix "./" p) declared;
          scanItems = if hasDefault then [ surf.dir ] else [ ];
        in
        if declared != null && !surf.additive then declaredItems else declaredItems ++ scanItems;

      mkCopyStep =
        s: surf: item:
        let
          destDir = "$out/${surf.dir}/${s.name}";
        in
        if item == surf.dir then
          ''
            mkdir -p "${destDir}"
            if [ -d ${s.src}/${surf.dir} ]; then
              cp -r --dereference ${s.src}/${surf.dir}/. "${destDir}/"
              chmod -R u+w "${destDir}"
            fi
          ''
        else
          ''
            mkdir -p "${destDir}"
            cp -r --dereference ${s.src}/${item} "${destDir}/"
            chmod -R u+w "${destDir}"
          '';

      copyStepsForSource =
        s:
        lib.concatMapStringsSep "\n" (
          surf:
          if s.${surf.flag} then
            lib.concatMapStringsSep "\n" (mkCopyStep s surf) (surfaceCopyItems s surf)
          else
            ""
        ) surfaces;

      copySteps = lib.concatMapStringsSep "\n" copyStepsForSource normSources;

      # --- MCP servers ---

      mcpSources = builtins.filter (s: s.includeMcp) normSources;
      mcpEntries = lib.concatMap (
        s:
        let
          mcpFilePath = s.src + "/.mcp.json";
          fileServers =
            if builtins.pathExists mcpFilePath then
              (builtins.fromJSON (builtins.readFile mcpFilePath)).mcpServers or { }
            else
              { };
          inlineServers = s.manifest.mcpServers or { };
          servers = fileServers // inlineServers;
        in
        lib.mapAttrsToList (k: v: {
          key = "${s.name}-${k}";
          value = v;
        }) servers
      ) mcpSources;
      mcpKeys = map (e: e.key) mcpEntries;
      mcpDupKeys = duplicatesIn mcpKeys;
      mcpServersOut =
        if mcpDupKeys != [ ] then
          throw "mkClaudeHookRouterPlugin: MCP server key collision after '<name>-' prefixing: ${lib.concatStringsSep ", " mcpDupKeys}"
        else
          builtins.listToAttrs (
            map (e: {
              name = e.key;
              inherit (e) value;
            }) mcpEntries
          );

      # --- version stamping (per mkSrcDigest's own documented multi-source form) ---

      digest = versionLib.mkSrcDigest (
        map (
          s:
          builtins.path {
            path = s.src;
            name = "${s.name}-src";
          }
        ) normSources
      );
      version = "${declared}+${digest}";

      pluginManifestFile = pkgs.writeText "plugin.json" (
        builtins.toJSON {
          inherit name description version;
        }
      );
      hooksJsonFile = pkgs.writeText "hooks.json" (builtins.toJSON { hooks = hooksJsonEvents; });
      routerConfigFile = pkgs.writeText "router-config.json" (builtins.toJSON routerConfig);
      mcpJsonFile = pkgs.writeText ".mcp.json" (builtins.toJSON { mcpServers = mcpServersOut; });
    in
    pkgs.runCommand "claude-hook-router-plugin-${name}" { } ''
      mkdir -p "$out/.claude-plugin"
      cp ${pluginManifestFile} "$out/.claude-plugin/plugin.json"

      mkdir -p "$out/hooks"
      cp ${hooksJsonFile} "$out/hooks/hooks.json"

      cp ${routerConfigFile} "$out/router-config.json"

      cp ${mcpJsonFile} "$out/.mcp.json"

      ${copySteps}

      # Real-copy invariant (§1.2/§2.6): no symlink anywhere under $out may
      # resolve outside $out — every copy above already dereferences, this is
      # a build-time assertion that none slipped through.
      if find "$out" -type l | grep -q .; then
        echo "mkClaudeHookRouterPlugin: refusing to produce a symlink under \$out (real-copy invariant violated):" >&2
        find "$out" -type l >&2
        exit 1
      fi
    '';
in
{
  inherit
    mkClaudePlugin
    mkClaudeMarketplace
    mkDirectoryMarketplaceSettings
    mkClaudeHookRouterPlugin
    ;
}
