# The headline drift-equality proof (bead pg2-r4cfy, D1). Wired into the base
# flake `checks.python-lock-version-drift`.
#
# A build passing does NOT prove the drift is closed — a name-match builder also
# builds. This check pins `six` in uv.lock to a version DIFFERENT from what base
# nixpkgs carries and asserts the shipped artifact reports the LOCK version, and
# that the pin still differs from nixpkgs (so it fails loudly to "re-pin" if
# nixpkgs ever converges rather than silently ceasing to discriminate). It is RED
# under the old name-match builder and GREEN under uv2nix.
#
# It also carries the only Tier-1 slice of AC2/AC3: it asserts the relocated
# runtime version stamp took (app --version is stamped, not the `0.0.0` placeholder).
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
    name = "py-lock-pin";
    src = ./fixtures/py-lock-pin;
    versionInitFile = "src/py_lock_pin/__init__.py";
  };
in
pkgs.runCommand "python-lock-version-drift"
  {
    nativeBuildInputs = [ app ];
    nixpkgsSix = pkgs.python3.pkgs.six.version;
    lockedSix = "1.16.0";
  }
  ''
    output=$(${app}/bin/py-lock-pin)
    echo "$output"
    six_ver=$(printf '%s\n' "$output" | sed -n 's/^six_version=//p')
    app_ver=$(printf '%s\n' "$output" | sed -n 's/^app_version=//p')
    app_dunder=$(printf '%s\n' "$output" | sed -n 's/^app_dunder_version=//p')

    # Discriminator: if nixpkgs ever converges onto the pin, this fixture stops
    # distinguishing lock-driven from name-match — fail loudly to force a re-pin.
    if [ "$lockedSix" = "$nixpkgsSix" ]; then
      echo "FAIL: fixture no longer discriminates — nixpkgs six ($nixpkgsSix) == lock pin ($lockedSix); re-pin the fixture to an older six"
      exit 1
    fi

    # D1: the shipped dependency version equals the LOCK, not the nixpkgs version.
    if [ "$six_ver" != "$lockedSix" ]; then
      echo "FAIL: shipped six=$six_ver != lock=$lockedSix (nixpkgs has $nixpkgsSix) — build is NOT lock-driven (drift!)"
      exit 1
    fi

    # AC2/AC3 (Tier-1 slice): the relocated runtime version stamp took on BOTH
    # surfaces — importlib.metadata (app_version) and the literal __init__.py
    # __version__ (app_dunder). A non-normalized project name would silently leave
    # both at 0.0.0 (see the fixture's underscore name), so this also guards the
    # PEP 503 normalization fix.
    for v in "$app_ver" "$app_dunder"; do
      case "$v" in
        "" | 0.0.0)
          echo "FAIL: app version not stamped (got '$v') — version relocation regressed"
          exit 1
          ;;
      esac
      if ! printf '%s\n' "$v" | grep -Eq '\+'; then
        echo "FAIL: app version '$v' missing the +<digest> local segment"
        exit 1
      fi
    done

    echo "OK: lock-driven (shipped six=$six_ver, nixpkgs carries $nixpkgsSix); runtime version stamped (metadata=$app_ver dunder=$app_dunder)"
    touch $out
  ''
