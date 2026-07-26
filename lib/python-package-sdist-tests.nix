# sdist build-path coverage for lib/python-package.nix (ADR 0022, bead
# pg2-abgyl). Wired into the base flake `checks.python-sdist-build-path`.
#
# The builder resolves with `sourcePreference = "wheel"`, so the pre-existing
# fixtures (six / eventsourcing — both ship universal wheels) ALWAYS take the
# wheel path and never exercise the sdist build + `pyproject-build-systems`
# (setuptools) overlay. This check closes that gap: fixture `py-sdist-only`
# depends on `termcolor==1.1.0`, a release that ships ONLY an sdist on PyPI (no
# wheel). With no wheel available, a successful build PROVES uv2nix went through
# the sdist build + build-systems overlay — there is no other way to obtain
# termcolor. The check builds the app and asserts `import termcolor` works at
# runtime and reports the locked version.
#
# Discriminator (mirrors the drift check's "re-pin" loud-fail): it asserts the
# fixture lock still has an sdist and NO wheel for termcolor, so if that release
# ever gains a wheel — which would silently route the build back through the
# wheel path and stop exercising the sdist build — the check fails loudly telling
# you to re-pin to another wheel-less release.
#
# This is a build+run check (not a lib.runTests eval check), because proving the
# sdist actually builds and imports requires building the venv and running python.
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
    name = "py-sdist-only";
    src = ./fixtures/py-sdist-only;
    versionInitFile = "src/py_sdist_only/__init__.py";
  };
  lockText = builtins.readFile ./fixtures/py-sdist-only/uv.lock;
in
pkgs.runCommand "python-sdist-build-path"
  {
    nativeBuildInputs = [ app ];
    inherit lockText;
    lockedTermcolor = "1.1.0";
  }
  ''
    # Discriminator: the fixture only forces the sdist path while its locked
    # termcolor release has an sdist and no wheel. If a wheel ever appears, the
    # build would silently take the wheel path — fail loudly to force a re-pin.
    if ! printf '%s' "$lockText" | grep -q '^sdist = '; then
      echo "FAIL: fixture lock has no sdist for termcolor — cannot exercise the sdist path; re-pin the fixture"
      exit 1
    fi
    if printf '%s' "$lockText" | grep -q 'wheels = \['; then
      echo "FAIL: fixture no longer discriminates — termcolor $lockedTermcolor now ships a wheel in the lock; re-pin the fixture to another wheel-less release"
      exit 1
    fi

    output=$(${app}/bin/py-sdist-only)
    echo "$output"

    # The dep is wheel-less, so a successful import proves it was built from the
    # sdist through the pyproject-build-systems overlay (the previously
    # unexercised path).
    if ! printf '%s\n' "$output" | grep -q "^termcolor_version=$lockedTermcolor\$"; then
      echo "FAIL: termcolor did not resolve from uv.lock at $lockedTermcolor"
      exit 1
    fi
    if ! printf '%s\n' "$output" | grep -q '^termcolor_module=termcolor$'; then
      echo "FAIL: termcolor did not import at runtime (sdist build failed to produce an importable module)"
      exit 1
    fi
    echo "OK: wheel-less dep built from sdist via pyproject-build-systems and imported (sdist path exercised)"
    touch $out
  ''
