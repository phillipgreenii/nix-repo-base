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
    # Derived from the wrapped package's own name so the per-source digest that
    # ADR 0006/0011 put in the derivation version survives into this store path
    # and stays visible in nvd's "Package changes" report. A bare
    # "pg-go-mutate-wrapped" dropped it, so an upgrade of the tool showed as an
    # unversioned path change.
    name = "${cfg.package.name}-wrapped";
    paths = [ cfg.package ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    # PG_GO_MUTATE_GOMU_VERSION is the other half of spec E1: this module is the
    # only place that knows which gomu is pinned, and the wrapper aborts if the
    # engine reports a different version. Without it, a `gomu version dev` build
    # — the unattributable kind every measurement behind this tool was taken
    # with — is accepted silently.
    postBuild = ''
      wrapProgram $out/bin/pg-go-mutate \
        --set PG_GO_MUTATE_GOMU ${getExe cfg.gomuPackage} \
        --set PG_GO_MUTATE_GOMU_VERSION ${cfg.gomuPackage.version}
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

    # Without this the tldr page is built into the store and reaches nobody:
    # `tldr pg-go-mutate` fails (spec T3). Sourced from cfg.package rather than
    # `wrapped` because the page lives in the script derivation; symlinkJoin
    # would resolve it too, but naming the origin keeps it obvious.
    programs.tldr.customPages.pg-go-mutate = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-go-mutate.md";
    };
  };
}
