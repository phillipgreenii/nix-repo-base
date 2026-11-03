# Pure script builders for the pg-reconcile-repo module.
# Mirrors modules/pg-git-check-identity/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  # The git fixture harness lives outside the script's `src`, so the sandboxed
  # bats check cannot see it unless it is passed in as `testSupport` (mkBash*
  # copies *.bash files into BATS_SUPPORT_PATH and exports the directory as
  # TEST_SUPPORT). Same mechanism as modules/pg-git-check-identity/scripts.nix.
  testSupport = pkgs.runCommand "pg-reconcile-repo-test-support" { } ''
    mkdir -p $out
    cp ${../../lib/scripts/git-fixture-harness.bash} $out/git-fixture-harness.bash
  '';

  pg-reconcile-repo = pkgs.callPackage ./pg-reconcile-repo {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport;
  };

  allScripts = [
    pg-reconcile-repo
  ];
in
{
  inherit pg-reconcile-repo;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-reconcile-repo = pg-reconcile-repo.check;
  };
}
