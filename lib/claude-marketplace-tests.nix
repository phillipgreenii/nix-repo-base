# Unit tests for lib/claude-marketplace.nix (a set of { expr; expected; } cases).
# Run via `lib.runTests` (wired into flake `checks.claude-marketplace-lib`).
#
# These are PURE-EVAL tests: constructing the mkClaudePlugin/mkClaudeMarketplace
# derivations is lazy (pkgs.runCommand is not forced), so we read their `passthru`
# + the eval-time stamped version WITHOUT building anything. mkDirectoryMarketplaceSettings
# is fully pure.
{ pkgs }:
let
  inherit (pkgs) lib;
  versionLib = import ./version.nix;
  builders = import ./claude-marketplace.nix { inherit pkgs lib; };
  inherit (builders) mkClaudePlugin mkClaudeMarketplace mkDirectoryMarketplaceSettings;

  fixture = ./tests/claude-marketplace-fixture;

  marketplace = mkClaudeMarketplace { src = fixture; };
  marketplaceCustomSuffix = mkClaudeMarketplace {
    src = fixture;
    nameSuffix = "-zr";
  };

  pluginA = mkClaudePlugin { src = fixture + "/plug-a"; };
  pluginB = mkClaudePlugin { src = fixture + "/plug-b"; };

  # Recompute the expected per-plugin digest exactly as the builder does
  # (mkSrcDigest of the content-addressed builtins.path of the plugin subtree).
  digestOf =
    name: src:
    versionLib.mkSrcDigest (
      builtins.path {
        path = src;
        name = "${name}-src";
      }
    );
  digestA = digestOf "plug-a" (fixture + "/plug-a");
  digestB = digestOf "plug-b" (fixture + "/plug-b");

  pluginByName = name: lib.findFirst (p: p.name == name) (throw "no ${name}") marketplace.plugins;

  settings = mkDirectoryMarketplaceSettings {
    inherit marketplace;
    path = "/home/test/.local/share/pgii-marketplaces/fixture-repo-marketplace-local";
  };

  settingsWithOverrides = mkDirectoryMarketplaceSettings {
    inherit marketplace;
    path = "/some/path";
    enabled = {
      # key-form override turns plug-a OFF (defaultEnabled true)
      "plug-a@fixture-repo-marketplace-local" = false;
      # bare-name override turns plug-b ON (defaultEnabled absent ⇒ false)
      "plug-b" = true;
    };
  };
in
{
  # --- marketplaceName suffixing ---
  testMarketplaceNameDefaultSuffix = {
    expr = marketplace.marketplaceName;
    expected = "fixture-repo-marketplace-local";
  };
  testMarketplaceNameCustomSuffix = {
    expr = marketplaceCustomSuffix.marketplaceName;
    expected = "fixture-repo-marketplace-zr";
  };

  # --- version = <declared>+<digest> (per-plugin, content-derived) ---
  testPluginVersionDeclaredPlusDigest = {
    expr = (pluginByName "plug-a").version;
    expected = "1.0.0+${digestA}";
  };
  testPluginBVersionDeclaredPlusDigest = {
    expr = (pluginByName "plug-b").version;
    expected = "2.3.4+${digestB}";
  };
  # mkClaudePlugin agrees with mkClaudeMarketplace on the stamped version.
  testMkClaudePluginVersionMatches = {
    expr = pluginA.version;
    expected = (pluginByName "plug-a").version;
  };
  # The digest portion is 8 hex chars and the declared portion is preserved.
  testVersionDigestLength = {
    expr = builtins.stringLength digestA;
    expected = 8;
  };

  # --- defaultEnabled: present true, absent ⇒ false ---
  testDefaultEnabledTrue = {
    expr = (pluginByName "plug-a").defaultEnabled;
    expected = true;
  };
  testDefaultEnabledAbsentFalse = {
    expr = (pluginByName "plug-b").defaultEnabled;
    expected = false;
  };
  testMkClaudePluginDefaultEnabledAbsentFalse = {
    expr = pluginB.defaultEnabled;
    expected = false;
  };

  # --- passthru plugin key shape ---
  testPluginKeyShape = {
    expr = (pluginByName "plug-a").key;
    expected = "plug-a@fixture-repo-marketplace-local";
  };

  # --- mkDirectoryMarketplaceSettings key shapes ---
  testSettingsExtraKnownMarketplaces = {
    expr = settings.extraKnownMarketplaces."fixture-repo-marketplace-local".source;
    expected = {
      source = "directory";
      path = "/home/test/.local/share/pgii-marketplaces/fixture-repo-marketplace-local";
    };
  };
  testSettingsPluginsList = {
    expr = settings.plugins;
    expected = [
      "plug-a@fixture-repo-marketplace-local"
      "plug-b@fixture-repo-marketplace-local"
    ];
  };
  # enabledPlugins resolves to plugin defaultEnabled with no overrides.
  testSettingsEnabledPluginsDefaults = {
    expr = settings.enabledPlugins;
    expected = {
      "plug-a@fixture-repo-marketplace-local" = true;
      "plug-b@fixture-repo-marketplace-local" = false;
    };
  };

  # --- override resolution: key-form and bare-name ---
  testSettingsOverrideKeyForm = {
    expr = settingsWithOverrides.enabledPlugins."plug-a@fixture-repo-marketplace-local";
    expected = false;
  };
  testSettingsOverrideBareName = {
    expr = settingsWithOverrides.enabledPlugins."plug-b@fixture-repo-marketplace-local";
    expected = true;
  };
}
