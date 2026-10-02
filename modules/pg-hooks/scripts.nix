# Pure script builders for the pg-hooks module (per-clone hook bundle).
# Mirrors modules/pg-go-mutate/scripts.nix and modules/pg-test-runner/scripts.nix.
#
# Exposes (consumed by later tasks of the per-clone hook bundle program):
#   pg-hooks-lib   the shared library (bundle resolution, pointer, stamp, messages)
#   pg-hooks-run   the bundle's stage runner (internal: not a PATH package)
#   pg-hooks       the user-facing CLI (status, list, explain, run)
#   pg-hooks-install  the installer behind install-pre-commit-hooks (internal)
#   libDir         a directory holding pg-hooks-lib.bash, for the bundle's lib/
#   stubTemplate   stub.sh.in (@STAGE@ placeholder), rendered by the installer
#   checks.test-pg-hooks-lib, checks.test-pg-hooks-run, checks.test-pg-hooks,
#   checks.test-pg-hooks-install
{
  pkgs,
  bashBuilders,
}:
let
  # The git fixture harness, the stub template and the shared bats helper are
  # outside every script's `src`, so the sandboxed bats checks cannot see them
  # unless they are passed in as `testSupport` (mkBash* copies *.bash files into
  # BATS_SUPPORT_PATH and exports the directory as TEST_SUPPORT).
  testSupport = pkgs.runCommand "pg-hooks-test-support" { } ''
    mkdir -p $out
    cp ${../../lib/scripts/git-fixture-harness.bash} $out/git-fixture-harness.bash
    cp ${./stub.sh.in} $out/stub.sh.in
    cp ${./test-support/pg-hooks-test-helper.bash} $out/pg-hooks-test-helper.bash
  '';

  pg-hooks-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  pg-hooks-run = pkgs.callPackage ./pg-hooks-run {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-hooks-lib testSupport;
  };

  pg-hooks = pkgs.callPackage ./pg-hooks {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-hooks-lib testSupport;
  };

  pg-hooks-install = pkgs.callPackage ./pg-hooks-install {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-hooks-lib testSupport;
    stubTemplate = ./stub.sh.in;
  };

  allScripts = [
    pg-hooks-run
    pg-hooks
    pg-hooks-install
  ];

  libDir = pkgs.runCommand "pg-hooks-lib-dir" { } ''
    mkdir -p $out
    cp ${pg-hooks-lib.lib} $out/pg-hooks-lib.bash
  '';
in
{
  inherit
    pg-hooks-lib
    pg-hooks-run
    pg-hooks
    pg-hooks-install
    libDir
    ;

  stubTemplate = ./stub.sh.in;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-hooks-lib = pg-hooks-lib.check;
    test-pg-hooks-run = pg-hooks-run.check;
    test-pg-hooks = pg-hooks.check;
    test-pg-hooks-install = pg-hooks-install.check;
  };
}
