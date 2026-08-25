# pg-test-runner home-manager module — installs the label-driven direct test
# runner on PATH and lets a machine customize its default configuration
# registry (spec section 2.1).
#
# The package is sourced from pkgs.pg-test-runner via this flake's
# overlays.default (mirrors home/pn and home/pg-go-mutate). It already ships
# a fully usable baked-in default (the same nix value as this module's
# `config` option default), so this module's ONLY job when a machine wants
# the STOCK registry is to put the package on PATH; the wrap-and-rebake below
# only matters once a machine actually customizes `phillipgreenii.pg-test-runner.config`.
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
  cfg = config.phillipgreenii.pg-test-runner;

  # pkgs.formats.json (spec section 2.1: "rendered via pkgs.formats.json").
  jsonFormat = pkgs.formats.json { };
  configJson = jsonFormat.generate "pg-test-runner-config.json" cfg.config;

  # Re-wrap so a machine-customized `cfg.config` actually takes effect: the
  # base package's own baked-in default (set at flake-build time, before any
  # home-manager option exists) is fixed, so a consumer that only installed
  # `pkgs.pg-test-runner` directly would never see a customization made here.
  # `--set` (unconditional), not `--suffix`: this IS the default the wrapper
  # ships with, not a fallback — a caller's `--config` flag or
  # `$PG_TEST_RUNNER_CONFIG` still wins per the documented precedence, since
  # both are checked in pg-test-runner.sh BEFORE it ever reads this baked var.
  wrapped = pkgs.symlinkJoin {
    # Derived from the wrapped package's own name so the per-source digest
    # (ADR 0006/0011) survives into this store path (mirrors
    # home/pg-go-mutate's `wrapped`).
    name = "${cfg.package.name}-wrapped";
    paths = [ cfg.package ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postBuild = ''
      wrapProgram $out/bin/pg-test-runner \
        --set PG_TEST_RUNNER_DEFAULT_CONFIG ${configJson}
    '';
  };
in
{
  options.phillipgreenii.pg-test-runner = {
    enable = mkEnableOption "pg-test-runner, the label-driven direct test runner";
    package = mkPackageOption pkgs "pg-test-runner" { };

    config = mkOption {
      inherit (jsonFormat) type;
      default = import ../../modules/pg-test-runner/config.nix;
      description = ''
        The pg-test-runner configuration registry (schema version 1; see
        docs/superpowers/specs/2026-08-24-pg-test-runner-design.md section
        2.1). Defaults to the shared machine-wide registry that ships with
        the package. A machine or repo overriding this MUST preserve
        `version = 1` and SHOULD extend (not replace) `languages`/`ignore`/
        `nonUnitLabels` — e.g. `cfg.config // { ignore = cfg.config.ignore
        ++ [ "some-repo-specific-path/" ]; }` — rather than restating the
        whole registry.
      '';
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ wrapped ];

    # Without this the tldr page is built into the store and reaches nobody
    # (mirrors home/pg-go-mutate's identical registration).
    programs.tldr.customPages.pg-test-runner = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-test-runner.md";
    };
  };
}
