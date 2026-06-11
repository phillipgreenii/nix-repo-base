# Proves mkBashScript's derivation identity is independent of the repo git rev:
# same src + two different self.rev => identical drvPath. Wired into
# flake `checks.bash-version-rev-independent`.
#
# Also proves library-content sensitivity (transitivity): changing a sourced
# library's content changes the script's drvPath.
{ pkgs }:
let
  inherit (pkgs) lib;
  mk =
    rev:
    (import ./bash-builders.nix {
      inherit pkgs lib;
      self = {
        inherit rev;
        lastModifiedDate = "20260101000000";
        narHash = "sha256-AAA";
      };
    }).mkBashScript
      {
        name = "demo";
        src = ./fixtures/demo;
        description = "demo script for rev-independence test";
      };
  drvA = (mk "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").script.drvPath;
  drvB = (mk "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").script.drvPath;

  # Library-sensitivity test: changing a sourced library's content must change
  # the script's drvPath (srcDigest covers library store paths transitively).
  mkWithLib =
    libContent:
    (import ./bash-builders.nix {
      inherit pkgs lib;
      self = {
        rev = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        lastModifiedDate = "20260101000000";
        narHash = "sha256-AAA";
      };
    }).mkBashScript
      {
        name = "demo";
        src = ./fixtures/demo;
        description = "demo";
        libraries = [ { lib = pkgs.writeText "mylib.bash" libContent; } ];
      };
  drvLib1 = (mkWithLib "echo lib-a").script.drvPath;
  drvLib2 = (mkWithLib "echo lib-b").script.drvPath;
in
pkgs.runCommand "bash-version-rev-independent"
  {
    inherit
      drvA
      drvB
      drvLib1
      drvLib2
      ;
  }
  ''
    if [ "$drvA" != "$drvB" ]; then
      echo "FAIL: script drvPath depends on repo rev:"; echo "  $drvA"; echo "  $drvB"; exit 1
    fi
    echo "OK: script drvPath is rev-independent"

    if [ "$drvLib1" = "$drvLib2" ]; then
      echo "FAIL: library content does not affect drvPath"; echo "  $drvLib1"; exit 1
    fi
    echo "OK: script drvPath changes when library content changes"

    touch $out
  ''
