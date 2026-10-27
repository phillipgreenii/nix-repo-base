# Pure script builders for the pg-check-grafana-alert-uids module (bead pg2-hlxmkk).
# Mirrors modules/pg-git-check-identity/scripts.nix. The script is built
# SELF-CONTAINED by flake-modules/pre-commit.nix (a consumer's perSystem `pkgs`
# lacks this repo's overlay), exactly like pg-git-check-identity.
{
  pkgs,
  bashBuilders,
}:
let
  pg-check-grafana-alert-uids = pkgs.callPackage ./pg-check-grafana-alert-uids {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    pg-check-grafana-alert-uids
  ];
in
{
  inherit pg-check-grafana-alert-uids;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-check-grafana-alert-uids = pg-check-grafana-alert-uids.check;
  };
}
