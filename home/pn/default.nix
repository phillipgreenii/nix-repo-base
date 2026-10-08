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
# Telemetry (ADR 0028, always built to emit, switched at RUN time): the tool is
# ALWAYS built and installed so it can emit telemetry; whether it does is a
# per-system runtime setting in `~/.config/pn/telemetry.toml`, checked on every
# run, so flipping it needs no rebuild or re-install. `phillipgreenii.pn.telemetry.*`
# renders that file on EVERY system (keys `enabled`, `endpoint`, `wrapper_path`),
# the same pattern as store.toml above, and ALWAYS adds the `pg-nix-log-wrapped`
# package to `home.packages`. `telemetry.enable` is the value of the file's
# `enabled` key (default false), NOT an install gate. The wrapper's default path
# is `lib.getExe pkgs.pg-nix-log-wrapped`, so the consuming machine repo MUST
# apply this flake's `overlays.default` (which carries `pkgs.pg-nix-log-wrapped`)
# on every system that enables `phillipgreenii.pn`, whether or not telemetry is
# on. This layer is below support-apps, so it MUST NOT call mkEmitterEnv: the
# machine repo sets `telemetry.endpoint` from the observability module's option
# (never a literal port) and leaves it null when there is no collector.
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

  # telemetry.toml is written on EVERY system: `enabled` and `wrapper_path`
  # always, `endpoint` when non-null (a null endpoint with enabled = true leaves
  # telemetry off and `pn workspace doctor` warns). `enabled = false` is the
  # runtime off switch; precedence against the flag, the environment and the
  # force-off controls is ADR 0028 "Runtime configuration contract". The
  # wrapper_path is honoured by pn under sudo only if it resolves into
  # /nix/store.
  telemetryToml = tomlFormat.generate "pn-telemetry.toml" (
    lib.optionalAttrs (tcfg.endpoint != null) { inherit (tcfg) endpoint; }
    // {
      enabled = tcfg.enable;
      wrapper_path = tcfg.wrapperPath;
    }
  );

  storeToml = tomlFormat.generate "pn-store.toml" {
    search_dirs = cfg.store.searchDirs;
    keep_days = cfg.store.keepDays;
    keep_count = cfg.store.keepCount;
    post_gc_clear_dirs = cfg.store.postGcClearDirs;
    post_gc_commands = cfg.store.postGcCommands;
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

      postGcClearDirs = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [ "/Users/me/.cache/some-binary-cache" ];
        description = ''
          pn store-deepclean: directories removed (rm -rf) right after the store
          GC, written to `store.toml` as `post_gc_clear_dirs`. For caches whose
          binaries may link to store paths the GC just deleted. A leading `~/`
          expands to $HOME at run time; anything else must be an absolute path.
          A missing directory is skipped. pn itself knows no tool-specific
          paths: the repo that owns the cache supplies them here.
        '';
      };

      postGcCommands = mkOption {
        type = types.listOf (types.listOf types.str);
        default = [ ];
        example = [
          [
            "some-tool"
            "--clear-cache"
          ]
        ];
        description = ''
          pn store-deepclean: argv-form commands run, in order, after the store
          GC and after `postGcClearDirs`. Written to `store.toml` as
          `post_gc_commands`. A failing command is reported and does not stop
          the others, but makes deepclean exit non-zero.
        '';
      };
    };

    telemetry = {
      enable = mkEnableOption "emitting pn / nix build telemetry (OpenTelemetry over OTLP/HTTP to a collector). This is the RUNTIME default written to `~/.config/pn/telemetry.toml` as `enabled`, not an install gate: the pn tooling and the pg-nix-log-wrapped package are always installed and `enabled = false` only turns emission off";

      endpoint = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "http://127.0.0.1:4318";
        description = ''
          OTLP/HTTP collector endpoint written to `~/.config/pn/telemetry.toml`
          (omitted when null). Telemetry is ON only when the file's `enabled`
          (from `enable`) is true AND an endpoint resolves (`--otlp-endpoint`,
          then `OTEL_EXPORTER_OTLP_ENDPOINT`, then this file); null leaves it off.
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
    # Always built to emit: the wrapper is installed on every system, enabled or
    # not (ADR 0028); whether it emits is the runtime `enabled` key.
    home.packages = [
      cfg.package
      pkgs.pg-nix-log-wrapped
    ];

    # Install store config when searchDirs or any post-GC hook is set
    # (keepDays/keepCount default to the tool's prior hardcoded 14/3).
    home.file =
      lib.optionalAttrs
        (cfg.store.searchDirs != [ ] || cfg.store.postGcClearDirs != [ ] || cfg.store.postGcCommands != [ ])
        {
          ".config/pn/store.toml".source = storeToml;
        }
      // {
        ".config/pn/telemetry.toml".source = telemetryToml;
      };
  };
}
