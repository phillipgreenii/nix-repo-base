# Proves mkPythonPackage's nvd-visible derivation `version` carries the per-source
# content digest (ADR 0011) — now stamped on the WRAPPER derivation under the
# uv2nix builder (ADR 0022). Evaluating `.version` does not build the package or
# force loadWorkspace, so demo-py needs no uv.lock. Wired into flake
# `checks.python-version-digest`.
#
# Curried: receives the uv2nix inputs (the builder's outer stage) plus pkgs.
{
  pkgs,
  uv2nix,
  pyproject-nix,
  pyproject-build-systems,
}:
let
  inherit (pkgs) lib;
  inherit ((import ./version.nix)) mkSrcDigest;
  builders =
    import ./python-package.nix
      {
        inherit uv2nix pyproject-nix pyproject-build-systems;
      }
      {
        inherit pkgs lib mkSrcDigest;
      };
  result = builders.mkPythonPackage {
    name = "demo-py";
    src = ./fixtures/demo-py;
  };
  inherit (result) version;
  expectedDigest = mkSrcDigest ./fixtures/demo-py;
in
pkgs.runCommand "python-version-digest"
  {
    inherit version expectedDigest;
  }
  ''
    # AC1: the nvd-visible version is `0.0.0-<srcDigest>`, stamped on the wrapper.
    case "$version" in
      0.0.0-*) ;;
      *)
        echo "FAIL: python wrapper version lacks the '0.0.0-' digest prefix:"
        echo "  $version"
        exit 1
        ;;
    esac
    if [ "$version" != "0.0.0-$expectedDigest" ]; then
      echo "FAIL: python wrapper version digest != mkSrcDigest src:"
      echo "  version=$version  expected=0.0.0-$expectedDigest"
      exit 1
    fi
    echo "OK: python wrapper derivation version carries the per-source digest ($version)"
    touch $out
  ''
