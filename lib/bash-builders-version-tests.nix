# Proves mkBashScript's derivation identity is independent of the repo git rev:
# same src + two different self.rev => identical drvPath. Wired into
# flake `checks.bash-version-rev-independent`.
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
in
pkgs.runCommand "bash-version-rev-independent" { inherit drvA drvB; } ''
  if [ "$drvA" != "$drvB" ]; then
    echo "FAIL: script drvPath depends on repo rev:"; echo "  $drvA"; echo "  $drvB"; exit 1
  fi
  echo "OK: script drvPath is rev-independent"; touch $out
''
