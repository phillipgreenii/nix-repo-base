# Pure script builders for the lint-grafana-dashboards module (bead pg2-rkgr6e).
# Mirrors modules/pg-check-grafana-alert-uids/scripts.nix. The lint is NOT a
# prek hook; dashboard packages in other repos wire it into their own checks.
{
  pkgs,
  bashBuilders,
}:
let
  lint-grafana-dashboards = pkgs.callPackage ./lint-grafana-dashboards {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };

  allScripts = [
    lint-grafana-dashboards
  ];
in
{
  inherit lint-grafana-dashboards;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-lint-grafana-dashboards = lint-grafana-dashboards.check;
  };
}
