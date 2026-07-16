# Lock-driven resolution regression for lib/python-package.nix (beads
# pg2-gjwpl -> pg2-r4cfy). Wired into the base flake `checks.python-resolve-lock-driven`.
#
# The OLD assertions (allowMissingDeps / throw on a nixpkgs name miss) are
# SUPERSEDED by uv2nix. The regression INTENT survives here as a POSITIVE proof:
# a dependency that nixpkgs lacks by name (`eventsourcing`) MUST still resolve —
# now from uv.lock — and import at runtime. This is a build+run check (not a
# lib.runTests eval check), because proving the import requires building the venv
# and running python.
#
# NOTE: the complementary NEGATIVE (fail-loud on an unresolvable lock / missing
# lock) is DEFERRED to the Tier-2/3 consumer follow-up. Empirically, uv2nix
# surfaces those as build-time or non-tryEval-catchable eval errors, so they
# cannot be expressed as a green Tier-1 flake check hermetically (a build-attempt
# harness — `pn workspace build` asserting failure — is the right tool).
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
  app = builders.mkPythonPackage {
    name = "py-missing-dep";
    src = ./fixtures/py-missing-dep;
    versionInitFile = "src/py_missing_dep/__init__.py";
  };
in
pkgs.runCommand "python-resolve-lock-driven"
  {
    nativeBuildInputs = [ app ];
  }
  ''
    output=$(${app}/bin/py-missing-dep)
    echo "$output"
    # eventsourcing is absent from nixpkgs by name; the old name-match builder
    # could not resolve it. Under uv2nix it resolves from uv.lock and imports.
    if ! printf '%s\n' "$output" | grep -q '^eventsourcing_version=9.4.6$'; then
      echo "FAIL: eventsourcing (absent from nixpkgs) did not resolve from uv.lock at 9.4.6"
      exit 1
    fi
    if ! printf '%s\n' "$output" | grep -q '^eventsourcing_module=eventsourcing$'; then
      echo "FAIL: eventsourcing did not import at runtime"
      exit 1
    fi
    echo "OK: absent-from-nixpkgs dep resolved from uv.lock and imported (lock-driven)"
    touch $out
  ''
