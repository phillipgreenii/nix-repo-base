# pg-go-mutate-tui home-manager module — installs the interactive,
# file-granular resumable mutation-testing TUI and its JSON settings file.
#
# The package is sourced from pkgs.pg-go-mutate-tui via this flake's
# overlays.default (mirrors home/pjira, home/pg-go-mutate, home/pg-test-runner).
#
# Deliberately its OWN module file, NOT folded into home/pg-go-mutate/default.nix
# -- that file already carries the gomu-engine-pin logic for the unrelated bash
# tool, and keeping the TUI's module separate keeps diffs clean, matching this
# repo's one-program-per-module convention (pn, pjira, pg-test-runner each get
# their own).
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib)
    mkEnableOption
    mkPackageOption
    mkOption
    mkIf
    ;
  cfg = config.phillipgreenii.pg-go-mutate-tui;
  jsonFormat = pkgs.formats.json { };
in
{
  options.phillipgreenii.pg-go-mutate-tui = {
    enable = mkEnableOption "pg-go-mutate-tui, the interactive mutation-testing TUI";
    package = mkPackageOption pkgs "pg-go-mutate-tui" { };

    settings = mkOption {
      inherit (jsonFormat) type;
      default = { };
      description = ''
        Written to `$XDG_CONFIG_HOME/pg-go-mutate-tui/config.json` -- a fixed
        cross-task contract: later bash code and the Go binary's
        `LoadStatic` both hard-code this path, so it MUST NOT change here
        without updating both. Recognized keys: `scanPaths`, `concurrency`,
        `lowWatermark`, `highWatermark`, `refillRetrySeconds`, `repoLabels`.
        Unknown keys are ignored by the tool with a warn log, not a hard
        failure.
      '';
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."pg-go-mutate-tui/config.json" = mkIf (cfg.settings != { }) {
      source = jsonFormat.generate "pg-go-mutate-tui-config.json" cfg.settings;
    };
  };
}
