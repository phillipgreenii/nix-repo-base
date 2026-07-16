# Proves the agent-support shape (D2): a consumer that INSTANTIATES the factory
# but ships no Python app (no uv.lock) still evaluates after currying. The
# returned attrset must expose `mkPythonPackage` without loadWorkspace ever
# running (loadWorkspace only fires inside a mkPythonPackage call). Wired into the
# base flake `checks.python-factory-currying-eval`.
{
  pkgs,
  uv2nix,
  pyproject-nix,
  pyproject-build-systems,
}:
let
  inherit (pkgs) lib;
  inherit ((import ./version.nix)) mkSrcDigest;
  factory =
    import ./python-package.nix
      {
        inherit uv2nix pyproject-nix pyproject-build-systems;
      }
      {
        inherit pkgs lib mkSrcDigest;
      };
  ok =
    (builtins.isAttrs factory)
    && (factory ? mkPythonPackage)
    && (builtins.isFunction factory.mkPythonPackage);
in
pkgs.runCommand "python-factory-currying-eval" { } (
  if ok then
    ''
      echo "OK: curried factory instantiates and exposes mkPythonPackage with no workspace/lock"
      touch $out
    ''
  else
    ''
      echo "FAIL: curried mkPythonBuilders did not expose mkPythonPackage"
      exit 1
    ''
)
