# jira home-manager module — installs the generic Jira CLI (Go).
# The package is sourced from pkgs.jira via this flake's overlays.default.
{
  config,
  lib,
  pkgs,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.jira;
in
{
  options.phillipgreenii.jira = {
    enable = mkEnableOption "generic jira access CLI";
    package = mkPackageOption pkgs "jira" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
