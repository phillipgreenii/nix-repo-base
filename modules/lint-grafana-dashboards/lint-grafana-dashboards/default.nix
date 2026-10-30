{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "lint-grafana-dashboards";
  src = ./.;
  description = "Lint Grafana dashboard JSON for fixed query windows, per-process label leaks, pushed-gauge sums, duplicate or empty panel titles and range queries on stat panels";
  public = true;
  # pushed-gauges.txt is read at runtime; the builder injects its store path.
  config = {
    PUSHED_GAUGES_FILE = ./pushed-gauges.txt;
  };
  runtimeDeps = [
    pkgs.jq
    pkgs.ripgrep
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.jq
    pkgs.ripgrep
    pkgs.coreutils
  ];
}
