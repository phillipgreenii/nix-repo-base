{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;
in
{
  # Mirrors darwin/modules/pn/default.nix exactly: phillipgreenii.observability.*
  # is a darwin/system-scope option declared in phillipgreenii-nix-support-apps
  # and cannot be set from a home-manager module. Unlike pn, pg-go-mutate-tui
  # also runs a /metrics endpoint and contributes an alert rule file, so all
  # three registrations are needed here, not just logSources.
  config = lib.mkIf (obs.enable or false) {
    phillipgreenii.observability = {
      logSources.pg-go-mutate-tui = { };
      metricsTargets.pg-go-mutate-tui = {
        port = 9464;
      };
      alertRuleFiles = [ ../../modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml ];
    };
  };
}
