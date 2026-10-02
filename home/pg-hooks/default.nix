# pg-hooks home-manager module -- installs the per-clone hook bundle CLI
# (status, list, explain, run) on PATH. Like gogate, there is no configuration
# registry to render: the bundle itself is per clone (built by each repo's
# install-pre-commit-hooks), so this module is a thin consumer that puts the
# package on PATH and registers its tldr page. The package is sourced from
# pkgs.pg-hooks via this flake's overlays.default (mirrors home/pg-test-runner).
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
    mkIf
    ;
  cfg = config.phillipgreenii.pg-hooks;
in
{
  options.phillipgreenii.pg-hooks = {
    enable = mkEnableOption "pg-hooks, the per-clone hook bundle CLI (status/list/explain/run)";
    package = mkPackageOption pkgs "pg-hooks" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Without this the tldr page is built into the store and reaches nobody
    # (mirrors home/gogate and home/pg-test-runner's identical registration).
    programs.tldr.customPages.pg-hooks = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-hooks.md";
    };
  };
}
