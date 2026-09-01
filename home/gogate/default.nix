# gogate home-manager module — installs the sequential Go validation gate
# (fmt/build/vet/test) on PATH. Unlike pg-go-mutate/pg-test-runner, there is
# no engine to bind and no configuration registry to render, so this module
# is a thin consumer: put the package on PATH and register its tldr page.
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
  cfg = config.phillipgreenii.gogate;
in
{
  options.phillipgreenii.gogate = {
    enable = mkEnableOption "gogate, the sequential Go validation gate (fmt/build/vet/test)";
    package = mkPackageOption pkgs "gogate" { };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Without this the tldr page is built into the store and reaches nobody
    # (mirrors home/pg-go-mutate and home/pg-test-runner's identical registration).
    programs.tldr.customPages.gogate = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/gogate.md";
    };
  };
}
