# Pure script builders for the pg-git-check-identity module.
# Mirrors modules/pg-test-runner/scripts.nix and modules/pg-go-mutate/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  # The git fixture harness lives outside the script's `src`, so the sandboxed
  # bats check cannot see it unless it is passed in as `testSupport` (mkBash*
  # copies *.bash files into BATS_SUPPORT_PATH and exports the directory as
  # TEST_SUPPORT). Same mechanism as modules/pnwf/scripts.nix.
  testSupport = pkgs.runCommand "pg-git-check-identity-test-support" { } ''
    mkdir -p $out
    cp ${../../lib/scripts/git-fixture-harness.bash} $out/git-fixture-harness.bash
  '';

  pg-git-check-identity = pkgs.callPackage ./pg-git-check-identity {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };

  allScripts = [
    pg-git-check-identity
  ];
in
{
  inherit pg-git-check-identity;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-git-check-identity = pg-git-check-identity.check;
  };
}
