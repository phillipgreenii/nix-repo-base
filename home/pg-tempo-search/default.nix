# pg-tempo-search home-manager module -- installs the Tempo search helper
# (always-explicit start/end window, sliced, merged) on PATH. A thin consumer
# like home/gogate: put the package on PATH and register its tldr page.
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
  cfg = config.phillipgreenii.pg-tempo-search;
in
{
  options.phillipgreenii.pg-tempo-search = {
    enable = mkEnableOption "pg-tempo-search, the Tempo search helper that always sets start and end";
    package = mkPackageOption pkgs "pg-tempo-search" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Without this the tldr page is built into the store and reaches nobody
    # (mirrors home/gogate's identical registration).
    programs.tldr.customPages.pg-tempo-search = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-tempo-search.md";
    };
  };
}
