# mkClaudeMarketplaceBuilders — factory for Claude Code marketplace packaging builders
#
# Takes { pkgs, lib } and returns
#   { mkClaudePlugin, mkClaudeMarketplace, mkDirectoryMarketplaceSettings }.
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
in
{
  inherit
    mkClaudePlugin
    mkClaudeMarketplace
    mkDirectoryMarketplaceSettings
    ;
}
