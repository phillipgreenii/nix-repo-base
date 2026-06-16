{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;
in
{
  # phillipgreenii.observability.logSources is a darwin/system-scope option declared
  # in phillipgreenii-nix-support-apps; it cannot be set from a home-manager module.
  # repo-base is otherwise darwin-free, so this is its first darwin module — kept to
  # exactly the logSources registration. pn writes its JSONL event stream to the
  # standard path ${XDG_STATE_HOME}/pn/events.jsonl, which the default `path` glob
  # (${env:XDG_STATE_HOME}/pn/*.jsonl) matches, so no overrides are needed. Guarded
  # on obs.enable so it is a no-op on machines without the observability stack.
  config = lib.mkIf (obs.enable or false) {
    phillipgreenii.observability.logSources.pn = { };
  };
}
