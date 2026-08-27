# Pure script builders for the pg-git-check-identity module.
# Mirrors modules/pg-test-runner/scripts.nix and modules/pg-go-mutate/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  pg-git-check-identity = pkgs.callPackage ./pg-git-check-identity {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    pg-git-check-identity
  ];
in
{
  inherit pg-git-check-identity;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-git-check-identity = pg-git-check-identity.check;
  };
}
