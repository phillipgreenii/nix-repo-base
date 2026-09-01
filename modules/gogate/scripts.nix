# Pure script builders for the gogate module.
# Mirrors modules/pg-go-mutate/scripts.nix and modules/pg-test-runner/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  gogate = pkgs.callPackage ./gogate {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    gogate
  ];
in
{
  inherit gogate;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-gogate = gogate.check;
  };
}
