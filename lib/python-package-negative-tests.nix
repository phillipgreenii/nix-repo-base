# Fail-loud NEGATIVE coverage for lib/python-package.nix (ADR 0022, bead
# pg2-abgyl). Wired into the base flake `checks.python-lock-fail-loud`.
#
# TIER: this is a Tier-2/3 "build-attempt" check, NOT a hermetic Tier-1
# lib.runTests check. uv2nix reports a missing / unparseable uv.lock as an eval
# error that builtins.tryEval cannot catch (see the note in
# python-package-resolve-tests.nix), so it cannot be asserted in-process. Instead
# this derivation ATTEMPTS the build of each broken input in a nested
# `nix-instantiate` (own throwaway chroot store, offline) and passes ONLY when
# that attempt FAILS with the expected message. It is a build-attempt rather than
# a pure eval check because success of the check is defined by a failing build.
#
# Inputs (fixtures under lib/fixtures):
#   py-no-lock  — pyproject.toml, NO uv.lock   -> must fail naming uv.lock
#   py-bad-lock — uv.lock that is not valid TOML -> must fail with a TOML parse error
# Control: py-sdist-only (valid lock) MUST evaluate cleanly under the same
# harness, proving the harness itself works and a failure above is caused by the
# lock, not by the harness (it would otherwise fail every input).
{
  pkgs,
  uv2nix,
  pyproject-nix,
  pyproject-build-systems,
}:
let
  system = pkgs.stdenv.hostPlatform.system;
  expr = fixture: ''
    let
      pkgs = import ${pkgs.path} { system = "${system}"; };
      inherit ((import ${./version.nix})) mkSrcDigest;
      inherit (pkgs) lib;
      pyproject-nix = import ${pyproject-nix} { inherit lib; };
      uv2nix = import ${uv2nix} { inherit lib pyproject-nix; };
      pyproject-build-systems = {
        overlays = import ${pyproject-build-systems} { inherit lib pyproject-nix uv2nix; };
      };
      builders = (import ${./python-package.nix} {
        inherit uv2nix pyproject-nix pyproject-build-systems;
      }) { inherit pkgs mkSrcDigest lib; };
    in
    (builders.mkPythonPackage { name = "py-neg"; src = ${fixture}; }).drvPath
  '';
in
pkgs.runCommand "python-lock-fail-loud"
  {
    nativeBuildInputs = [ pkgs.nix ];
    noLockExpr = expr "${./fixtures/py-no-lock}";
    badLockExpr = expr "${./fixtures/py-bad-lock}";
    controlExpr = expr "${./fixtures/py-sdist-only}";
  }
  ''
    export HOME=$TMPDIR/home NIX_STATE_DIR=$TMPDIR/state NIX_LOG_DIR=$TMPDIR/log
    mkdir -p $HOME
    store="local?root=$TMPDIR/chroot"
    attempt() {
      nix-instantiate --extra-experimental-features 'nix-command flakes' \
        --store "$store" --read-write-mode --eval --expr "$1" 2>&1
    }

    # Control must succeed.
    if ! res=$(attempt "$controlExpr"); then
      echo "FAIL: harness control (valid lock) did not evaluate:"; echo "$res"; exit 1
    fi
    echo "control ok: $res"

    expect_fail() { # label expr pattern
      if res=$(attempt "$2"); then
        echo "FAIL: $1 build attempt SUCCEEDED; it must fail loudly. Output: $res"; exit 1
      fi
      if ! printf '%s\n' "$res" | grep -q -- "$3"; then
        echo "FAIL: $1 failed but not with the expected message /$3/:"; echo "$res"; exit 1
      fi
      echo "OK: $1 failed loudly as expected (/$3/)"
    }
    expect_fail "missing uv.lock" "$noLockExpr" "uv.lock' does not exist"
    expect_fail "unparseable uv.lock" "$badLockExpr" "while parsing TOML"
    touch $out
  ''
