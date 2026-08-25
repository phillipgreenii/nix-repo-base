# Pure script builders for the pg-test-runner module.
# Mirrors modules/ul/scripts.nix and modules/pg-go-mutate/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  pg-test-runner = pkgs.callPackage ./pg-test-runner {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    pg-test-runner
  ];
in
{
  inherit pg-test-runner;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-test-runner = pg-test-runner.check;
  };
}
