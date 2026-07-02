# pjira home-manager module — installs the generic Jira CLI (Go).
# The package is sourced from pkgs.pjira via this flake's overlays.default.
{
  config,
  lib,
  pkgs,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.pjira;
in
{
  options.phillipgreenii.pjira = {
    enable = mkEnableOption "generic pjira access CLI";
    package = mkPackageOption pkgs "pjira" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
