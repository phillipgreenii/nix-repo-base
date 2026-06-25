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
  scriptA = (mk "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").script;
  scriptB = (mk "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb").script;
  drvA = scriptA.drvPath;
  drvB = scriptB.drvPath;

  # The nvd-visible derivation `version` must now carry the per-source digest
  # (ADR 0011): not the bare "0.0.0" placeholder, contains a "-" separator, and
  # is rev-independent (identical across two self.rev values).
  versionA = scriptA.version;
  versionB = scriptB.version;

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
      versionA
      versionB
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

    if [ "$versionA" = "0.0.0" ]; then
      echo "FAIL: script version is the bare placeholder, missing the digest:"; echo "  $versionA"; exit 1
    fi
    case "$versionA" in
      *-*) ;;
      *) echo "FAIL: script version does not contain a '-' digest separator:"; echo "  $versionA"; exit 1 ;;
    esac
    echo "OK: script version carries the per-source digest ($versionA)"

    if [ "$versionA" != "$versionB" ]; then
      echo "FAIL: script version depends on repo rev:"; echo "  $versionA"; echo "  $versionB"; exit 1
    fi
    echo "OK: script version is rev-independent"

    touch $out
  ''
