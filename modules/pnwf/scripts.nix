# Pure script builders for the pnwf (workforest work-cycle) module.
# Mirrors modules/ul/scripts.nix and modules/pn/scripts.nix.
#
# SKELETON (bead pg2-xs5cj task 2): only the shared `pnwf-lib` primitives
# exist so far. A later task adds the `pnwf` command itself and wires its
# mkBashScript result into `allScripts` (and `packages`/`tldr` below follow
# automatically).
{
  pkgs,
  bashBuilders,
}:
let
  pnwf-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  allScripts = [ ];
in
{
  inherit pnwf-lib;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pnwf-lib = pnwf-lib.check;
  };

  check = pkgs.runCommand "test-pnwf-scripts" { } ''
    echo ${pnwf-lib.check}
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
