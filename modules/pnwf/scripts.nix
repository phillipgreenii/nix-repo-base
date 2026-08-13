# Pure script builders for the pnwf (workforest work-cycle) module.
# Mirrors modules/ul/scripts.nix and modules/pn/scripts.nix.
#
# `pn` is a PARAMETER, threaded in from self.packages by flake.nix (mirroring
# ulScripts' update-locks-lib). It is deliberately NOT taken off `pkgs`: this
# flake's own `pkgs` applies only overlays.gomod2nix, and overlays.default —
# the only thing that surfaces `pn` as `pkgs.pn` — is exported for CONSUMERS
# and never applied here, so `inherit (pkgs) pn` would fail to evaluate.
{
  pkgs,
  bashBuilders,
  pn,
}:
let
  pnwf-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  pnwf = pkgs.callPackage ./pnwf {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pnwf-lib;
  };

  # wsplan: the read-only Stage A land-plan emitter. A SECOND, independent
  # command in this module, NOT a `pnwf` subcommand — sharing the module
  # directory (and pnwf-lib) is a packaging choice only.
  #
  # It receives the already-bound `pnwf` above, so the dependency runs one way
  # only: a circular script dependency makes nix evaluation recurse infinitely.
  wsplan = pkgs.callPackage ./wsplan {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      pnwf-lib
      pnwf
      pn
      ;
  };

  allScripts = [
    pnwf
    wsplan
  ];
in
{
  inherit pnwf-lib pnwf wsplan;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pnwf-lib = pnwf-lib.check;
    test-pnwf = pnwf.check;
    test-wsplan = wsplan.check;
  };

  check = pkgs.runCommand "test-pnwf-scripts" { } ''
    echo ${pnwf-lib.check}
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
