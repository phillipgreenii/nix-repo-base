{
  mkBashScript,
  pkgs,
}:

mkBashScript {
  name = "pg-check-grafana-alert-uids";
  src = ./.;
  description = "Reject a Grafana alerting provisioning file with an alert-rule or receiver uid over 40 characters, or a duplicate alert-rule uid";
  # Internal: only ever executed by the check-grafana-alert-uids prek hook that
  # flake-modules/pre-commit.nix defines. Not a PATH command.
  public = false;
  # yq-go (mikefarah v4) parses the YAML; coreutils and gnugrep are pinned so the
  # behavior does not depend on the host's BSD or GNU flavor. runtimeDeps are
  # appended with --suffix, so a machine that already has them keeps its own.
  runtimeDeps = [
    pkgs.yq-go
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.yq-go
    pkgs.coreutils
  ];
}
