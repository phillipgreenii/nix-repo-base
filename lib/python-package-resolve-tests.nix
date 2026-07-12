# Unit tests for lib/python-package.nix fail-fast dependency resolution
# (bead pg2-gjwpl). A set of { expr; expected; } cases run via lib.runTests and
# wired into the base flake `checks.python-resolve-fail-fast`.
#
# The fixture lib/fixtures/py-missing-dep declares a dependency that is absent
# from nixpkgs by name, so it exercises the else-branch of resolveDep.
{ pkgs }:
let
  inherit (pkgs) lib;
  inherit ((import ./version.nix)) mkSrcDigest;
  builders = import ./python-package.nix { inherit pkgs lib mkSrcDigest; };
  mk =
    args:
    builders.mkPythonPackage (
      {
        name = "py-missing-dep";
        src = ./fixtures/py-missing-dep;
      }
      // args
    );
  # Forcing the derivation's drvPath forces propagatedBuildInputs, which is where
  # an unresolved dependency throws (or is skipped under allowMissingDeps).
  resolves = pkg: (builtins.tryEval (builtins.seq pkg.drvPath true)).success;
in
{
  # allowMissingDeps = false (default): an unresolved dep MUST fail evaluation,
  # never silently drop. This is the core of the finding.
  testMissingDepFailsByDefault = {
    expr = resolves (mk { });
    expected = false;
  };

  # allowMissingDeps = true: the explicit escape hatch preserves the old
  # skip-with-warning behavior so a deliberate tolerance still evaluates.
  testMissingDepAllowedWithEscapeHatch = {
    expr = resolves (mk {
      allowMissingDeps = true;
    });
    expected = true;
  };
}
