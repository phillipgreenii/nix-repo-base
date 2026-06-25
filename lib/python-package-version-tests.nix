# Proves mkPythonPackage's nvd-visible derivation `version` carries the
# per-source content digest (ADR 0011): not the bare "0.0.0" placeholder and
# containing a "-" digest separator. This is the Python counterpart to
# bash-builders-version-tests.nix. Evaluating `.version` does not build the
# package. Wired into flake `checks.python-version-digest`.
{ pkgs }:
let
  inherit (pkgs) lib;
  inherit ((import ./version.nix)) mkSrcDigest;
  result = (import ./python-package.nix { inherit pkgs lib mkSrcDigest; }).mkPythonPackage {
    name = "demo-py";
    src = ./fixtures/demo-py;
  };
  inherit (result) version;
in
pkgs.runCommand "python-version-digest" { inherit version; } ''
  if [ "$version" = "0.0.0" ]; then
    echo "FAIL: python derivation version is the bare placeholder, missing the digest:"; echo "  $version"; exit 1
  fi
  case "$version" in
    *-*) ;;
    *) echo "FAIL: python derivation version does not contain a '-' digest separator:"; echo "  $version"; exit 1 ;;
  esac
  echo "OK: python derivation version carries the per-source digest ($version)"
  touch $out
''
