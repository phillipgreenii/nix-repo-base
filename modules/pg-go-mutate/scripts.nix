# Pure script builders for the pg-go-mutate module.
# Mirrors modules/pnwf/scripts.nix and modules/ul/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  pg-go-mutate-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  pg-go-mutate = pkgs.callPackage ./pg-go-mutate {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-go-mutate-lib;
  };

  pg-go-mutate-sweep = pkgs.callPackage ./pg-go-mutate-sweep {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-go-mutate-lib;
  };

  allScripts = [
    pg-go-mutate
    pg-go-mutate-sweep
  ];
in
{
  inherit pg-go-mutate-lib pg-go-mutate pg-go-mutate-sweep;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-go-mutate-lib = pg-go-mutate-lib.check;
    test-pg-go-mutate = pg-go-mutate.check;
    test-pg-go-mutate-sweep = pg-go-mutate-sweep.check;
  };

  check = pkgs.runCommand "test-pg-go-mutate-scripts" { } ''
    echo ${pg-go-mutate-lib.check}
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
