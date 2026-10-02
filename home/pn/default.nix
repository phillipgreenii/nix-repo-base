# PN home-manager module
#
# Provides the pn workspace management binary (Go).
# Workspace root is discovered at runtime (walk up CWD for pn-workspace.toml).
# Apply command and hooks live in pn-workspace.toml, not here.
#
# The pn package is sourced from pkgs.pn, which consuming flakes make
# available by adding this flake's overlays.default to nixpkgs.overlays.
# Override phillipgreenii.pn.package to substitute a different build.
#
# Observability: `pn workspace update` writes a structured JSONL event stream
# (run_start / project_result / run_end; skipped repos -> warn, failed -> error)
# to the standard path `${XDG_STATE_HOME}/pn/events.jsonl`, distinct from pn's
# human stdout transcript. Lines conform to the phillipgreenii JSONL standard
# (`time`/`level`/`msg`). The sibling `darwinModules.default` aggregate
# (`darwin/modules/pn/default.nix`) registers `phillipgreenii.observability.logSources.pn`
# so the file is collected into Loki; the default glob (`${env:XDG_STATE_HOME}/pn/*.jsonl`)
# matches it, so no path override is needed. That registration is inert until a
# machine flake imports `repo-base.darwinModules.default`.
#
# Telemetry (ADR 0028, optional, OFF by default): `phillipgreenii.pn.telemetry.*`
# renders `~/.config/pn/telemetry.toml` (endpoint + wrapper_path), the same
# pattern as store.toml above, and installs the `pg-nix-log-wrapped` package.
# With `telemetry.enable = false` (the default) NO file is written and the
# wrapper package is NOT added to `home.packages` (no closure bloat on machines
# without OTel). The wrapper's default path is `lib.getExe pkgs.pg-nix-log-wrapped`,
# so the consuming machine repo MUST apply this flake's `overlays.default`
# (which carries `pkgs.pg-nix-log-wrapped`); the default is lazy, so a machine
# that never enables telemetry evaluates fine without it. This layer is below
# support-apps, so it MUST NOT call mkEmitterEnv: the machine repo sets
# `telemetry.endpoint` from the observability module's option (never a literal
# port) and leaves it null when observability is off.
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
    types
    ;
  cfg = config.phillipgreenii.pn;

  # Generate store.toml through pkgs.formats.toml so values are serialized as
  # real TOML: hand-concatenating `"${d}"` broke on a searchDir containing a
  # quote or backslash (bead pg2-v6j3h). keep_days/keep_count are now options
  # rather than magic numbers baked into the string.
  tomlFormat = pkgs.formats.toml { };
  tcfg = cfg.telemetry;

  # telemetry.toml: only the keys that have a value are written, so a null
  # endpoint leaves telemetry OFF (an endpoint is what turns it on; precedence
  # in pn is --otlp-endpoint, OTEL_EXPORTER_OTLP_ENDPOINT, then this file).
  # The wrapper_path is honoured by pn under sudo only if it resolves into
  # /nix/store (ADR 0028).
  telemetryToml = tomlFormat.generate "pn-telemetry.toml" (
    lib.optionalAttrs (tcfg.endpoint != null) { inherit (tcfg) endpoint; }
    // {
      wrapper_path = tcfg.wrapperPath;
    }
  );

  storeToml = tomlFormat.generate "pn-store.toml" {
    search_dirs = cfg.store.searchDirs;
    keep_days = cfg.store.keepDays;
    keep_count = cfg.store.keepCount;
  };
in
{
  options.phillipgreenii.pn = {
    enable = mkEnableOption "pn personal-nix workspace tool";

    package = mkPackageOption pkgs "pn" { };

    store = {
      searchDirs = mkOption {
        type = types.listOf types.str;
        default = [ ];
        description = "Directories to search for Nix project roots in pn store-audit and pn store-deepclean. If empty, the tool defaults to $HOME.";
      };

      keepDays = mkOption {
        type = types.ints.unsigned;
        default = 14;
        description = "pn store-deepclean: keep generations newer than this many days.";
      };

      keepCount = mkOption {
        type = types.ints.unsigned;
        default = 3;
        description = "pn store-deepclean: keep at least this many most-recent generations regardless of age.";
      };
    };

    telemetry = {
      enable = mkEnableOption "pn / nix build telemetry (OpenTelemetry over OTLP/HTTP to a local collector). Off permanently when false: no config file is written and the pg-nix-log-wrapped package is not installed";

      endpoint = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "http://127.0.0.1:4318";
        description = ''
          OTLP/HTTP collector endpoint written to `~/.config/pn/telemetry.toml`.
          Telemetry is ON only when an endpoint resolves (flag, then
          `OTEL_EXPORTER_OTLP_ENDPOINT`, then this file); null leaves it off.
          The machine repo SHOULD derive this from the observability module's
          http port option instead of a literal.
        '';
      };

      wrapperPath = mkOption {
        type = types.str;
        default = lib.getExe pkgs.pg-nix-log-wrapped;
        defaultText = lib.literalExpression "lib.getExe pkgs.pg-nix-log-wrapped";
        description = ''
          Absolute path of the pg-nix-log-wrapped binary written to
          `~/.config/pn/telemetry.toml` as `wrapper_path`. The default needs the
          repo-base overlay (`overlays.default`) applied in the consuming flake.
          Under sudo pn uses it only if its resolved real path is under
          `/nix/store`; otherwise it runs unwrapped.
        '';
      };
    };
  };

  config = mkIf cfg.enable {
    # The wrapper package is added ONLY when telemetry is enabled.
    home.packages = [ cfg.package ] ++ lib.optional tcfg.enable pkgs.pg-nix-log-wrapped;

    # Install store config only when searchDirs is non-empty (unchanged gating;
    # keepDays/keepCount default to the tool's prior hardcoded 14/3).
    home.file =
      lib.optionalAttrs (cfg.store.searchDirs != [ ]) {
        ".config/pn/store.toml".source = storeToml;
      }
      // lib.optionalAttrs tcfg.enable {
        ".config/pn/telemetry.toml".source = telemetryToml;
      };
  };
}
