# Pure script builders for the pg-tempo-search module.
# Mirrors modules/gogate/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  pg-tempo-search = pkgs.callPackage ./pg-tempo-search {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    pg-tempo-search
  ];
in
{
  inherit pg-tempo-search;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    # The tests bind a loopback stub server; a sandboxed darwin build refuses
    # that unless the derivation opts in.
    test-pg-tempo-search = pg-tempo-search.check.overrideAttrs (_: {
      __darwinAllowLocalNetworking = true;
    });
  };
}
