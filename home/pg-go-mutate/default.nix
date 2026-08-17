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
  # The SAME string the script derivation bakes in as PGM_PINNED_GOMU_VERSION.
  # Imported rather than restated: two hand-maintained copies of a pinned version
  # is a second source of truth, and the copy nobody exercises is the one that
  # rots. See that file for the full contract.
  pinnedGomuVersion = import ../../modules/pg-go-mutate/pg-go-mutate/gomu-pin.nix;
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

    # The mechanism that keeps this module and the script's baked-in pin
    # AGREEING. repo-base cannot check it itself: pkgs.phillipgreenii.gomu lives
    # in phillipgreenii-nix-overlay, which consumes repo-base, so an input edge
    # back would be a cycle -- this module, evaluated in a consumer's pkgs, is the
    # only place both values are visible at once.
    #
    # Failing EVAL is deliberate, and it is the cheap direction. Left
    # unenforced, a gomu bump in the overlay would move the version this module
    # injects while the version baked into the shipped script stayed behind, and
    # the drift would surface only as a runtime abort for whoever installed
    # pkgs.pg-go-mutate directly -- the one consumer with no way to see why.
    # Both halves are one string in one file, so the fix is a one-line
    # coordinated change (that file, plus the overlay's sources.gomu).
    #
    # Compared with the `v` prefix normalized off both sides, which tolerates it
    # on the gomuPackage side ONLY: the overlay strips the prefix off the fetched
    # tag today, but that is its choice, not a contract. It does NOT make this
    # assertion tolerant of a v-prefixed PIN, and MUST NOT be read that way --
    # gomu-pin.nix rejects one at eval precisely because this normalization would
    # otherwise hide it. The runtime check strips `v` from the version the ENGINE
    # REPORTS and never from the expectation, so a pin written "v0.3.0" passes
    # here and then aborts every run on BOTH install paths. That failure would
    # not be spurious: it would be the only warning anyone gets, and it would
    # arrive after the artifact shipped.
    assertions = [
      {
        assertion = lib.removePrefix "v" cfg.gomuPackage.version == lib.removePrefix "v" pinnedGomuVersion;
        message =
          "phillipgreenii.pg-go-mutate: the pinned engine version disagrees with the one pg-go-mutate was built against. "
          + "gomuPackage (${cfg.gomuPackage.name}) reports version '${cfg.gomuPackage.version}', but the script bakes in '${pinnedGomuVersion}' "
          + "(modules/pg-go-mutate/pg-go-mutate/gomu-pin.nix in phillipg-nix-repo-base). "
          + "Bump that file to match the engine and relock, or point gomuPackage at the engine the tool is pinned to; "
          + "leaving them apart would make a direct install of pkgs.pg-go-mutate abort at runtime (spec E1).";
      }
    ];

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
