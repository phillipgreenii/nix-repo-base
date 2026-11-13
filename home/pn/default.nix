# PN home-manager module
#
# Provides the pn workspace management binary (Go).
# Workspace root is discovered at runtime (walk up CWD for pn-workspace.toml).
# Build and activate commands and hooks live in pn-workspace.toml, not here.
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
# renders that file on EVERY system (keys `enabled`, optional `spool_dir`,
# `wrapper_path`), the same pattern as store.toml above, and ALWAYS adds the `pg-nix-log-wrapped`
# package to `home.packages`. `telemetry.enable` is the value of the file's
# `enabled` key (default false), NOT an install gate. The wrapper's default path
# is `lib.getExe pkgs.pg-nix-log-wrapped`, so the consuming machine repo MUST
# apply this flake's `overlays.default` (which carries `pkgs.pg-nix-log-wrapped`)
# on every system that enables `phillipgreenii.pn`, whether or not telemetry is
# on. Telemetry is written to a local spool directory that a user-run collector
# ingests (no endpoint is involved): `telemetry.spool_dir` overrides the default
# `${XDG_STATE_HOME}/otel-spool`. `telemetry.endpoint` is DEPRECATED: accepted,
# ignored (never rendered) and reported through `warnings` until the machine
# repo drops it. This layer is below support-apps, so it MUST NOT call
# mkEmitterEnv.
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
  # always, `spool_dir` when non-null (null means the XDG default). The
  # deprecated `endpoint` option is never rendered. `enabled = false` is the
  # runtime off switch; precedence against the flag, the environment and the
  # force-off controls is ADR 0028 "Runtime configuration contract". The
  # wrapper_path is honoured by pn under sudo only if it resolves into
  # /nix/store.
  telemetryToml = tomlFormat.generate "pn-telemetry.toml" (
    lib.optionalAttrs (tcfg.spool_dir != null) { inherit (tcfg) spool_dir; }
    // {
      enabled = tcfg.enable;
      wrapper_path = tcfg.wrapperPath;
    }
  );

  # Activation check for a missing pn-workspace.toml (bead tc-w1iu3). pn REWRITES
  # that file (`pn workspace init` / `doctor --fix`), so Nix MUST NOT own it
  # (a home.file symlink would be read-only): this only WARNS, never fails and
  # never writes. Root resolution mirrors pn's own (internal/cli/workspace.go):
  # PN_WORKSPACE_ROOT first, else the nearest ancestor of the CWD holding
  # pn-workspace.toml. The script is exposed as
  # `config.home.activation.pnWorkspaceTomlWarn.data` so
  # tests/pn-workspace-toml-warning.nix can run the real script.
  workspaceTomlWarnScript = ''
    pn_root="''${PN_WORKSPACE_ROOT:-}"
    if [ -z "$pn_root" ]; then
      pn_dir="$PWD"
      while [ "$pn_dir" != "/" ] && [ ! -f "$pn_dir/pn-workspace.toml" ]; do
        pn_dir="$(dirname "$pn_dir")"
      done
      pn_root="$pn_dir"
    fi
    if [ ! -f "$pn_root/pn-workspace.toml" ]; then
      echo "warning: phillipgreenii.pn.enable is set but pn-workspace.toml was not found (PN_WORKSPACE_ROOT=\"''${PN_WORKSPACE_ROOT:-}\", searched up from $PWD); run 'pn workspace init' to create it" >&2
    fi
  '';

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
      enable = mkEnableOption "emitting pn / nix build telemetry (OpenTelemetry written to a local spool directory that a collector ingests). This is the RUNTIME default written to `~/.config/pn/telemetry.toml` as `enabled`, not an install gate: the pn tooling and the pg-nix-log-wrapped package are always installed and `enabled = false` only turns emission off";

      spool_dir = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "/Users/me/.local/state/otel-spool";
        description = ''
          Directory telemetry spool files are written to, rendered to
          `~/.config/pn/telemetry.toml` as `spool_dir` (omitted when null, which
          means the XDG default `''${XDG_STATE_HOME}/otel-spool`, else
          `$HOME/.local/state/otel-spool`). Must be an absolute path. The
          collector watches ONLY `''${XDG_STATE_HOME}/otel-spool`, so an override
          is never ingested unless the collector configuration is changed to
          match. A machine with `enable = true` and no collector accumulates
          spool files up to the sweep bounds.
        '';
      };

      endpoint = mkOption {
        type = types.nullOr types.str;
        default = null;
        visible = false;
        description = ''
          DEPRECATED and ignored: telemetry no longer uses an endpoint, so this
          is never written to `~/.config/pn/telemetry.toml`. Setting it emits a
          warning. Use `spool_dir` to relocate the spool. Will be removed.
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
    warnings =
      lib.optional (tcfg.endpoint != null)
        "phillipgreenii.pn.telemetry.endpoint is deprecated and ignored (telemetry uses a local spool directory now); remove it, and use phillipgreenii.pn.telemetry.spool_dir to relocate the spool.";

    # Always built to emit: the wrapper is installed on every system, enabled or
    # not (ADR 0028); whether it emits is the runtime `enabled` key.
    home = {
      packages = [
        cfg.package
        pkgs.pg-nix-log-wrapped
        # The reconcile-repo skill's step 4 runs this; it is exported by
        # overlays.default but nothing else puts it on the agent PATH.
        pkgs.pg-reconcile-repo
      ];

      # Warn (never fail, never write) when pn-workspace.toml is absent.
      activation.pnWorkspaceTomlWarn = {
        after = [ "writeBoundary" ];
        before = [ ];
        data = workspaceTomlWarnScript;
      };

      # Install store config when searchDirs or any post-GC hook is set
      # (keepDays/keepCount default to the tool's prior hardcoded 14/3).
      file =
        lib.optionalAttrs
          (cfg.store.searchDirs != [ ] || cfg.store.postGcClearDirs != [ ] || cfg.store.postGcCommands != [ ])
          {
            ".config/pn/store.toml".source = storeToml;
          }
        // {
          ".config/pn/telemetry.toml".source = telemetryToml;
        };
    };
  };
}
