# pg-go-mutate home-manager module — installs the Go mutation-testing diagnostic.
# The package is sourced from pkgs.pg-go-mutate via this flake's overlays.default.
#
# The ENGINE is bound here, not at package build time: this flake's own pkgs
# applies only overlays.gomod2nix, and overlays.default is exported for
# consumers and never applied here (the same constraint modules/pnwf/scripts.nix
# documents for pn). So pkgs.phillipgreenii.gomu is resolvable only in a
# consumer's pkgs — which is exactly where this module evaluates.
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
    getExe
    ;
  cfg = config.phillipgreenii.pg-go-mutate;
  # --set, not --suffix: the pin must be authoritative, so an ambient
  # ~/go/bin/gomu cannot substitute itself for the engine.
  wrapped = pkgs.symlinkJoin {
    name = "pg-go-mutate-wrapped";
    paths = [ cfg.package ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postBuild = ''
      wrapProgram $out/bin/pg-go-mutate \
        --set PG_GO_MUTATE_GOMU ${getExe cfg.gomuPackage}
    '';
  };
in
{
  options.phillipgreenii.pg-go-mutate = {
    enable = mkEnableOption "pg-go-mutate, the Go mutation-testing diagnostic";
    package = mkPackageOption pkgs "pg-go-mutate" { };
    # Forced only under mkIf cfg.enable, so a consumer that never enables the
    # feature never evaluates this and needs no overlay input.
    gomuPackage = mkPackageOption pkgs [ "phillipgreenii" "gomu" ] { };
  };

  config = mkIf cfg.enable {
    home.packages = [ wrapped ];
  };
}
