# pg-nix-shim nix-darwin module: puts the pg-nix-shim package (bin/nix and
# bin/darwin-rebuild, symlinks to pg-nix-log-wrapped) first on the system PATH,
# and writes the root-owned configuration the shim reads when it runs as root.
#
# Off by default. Part of darwinModules.default, so importing the aggregate
# changes nothing until a machine sets `phillipgreenii.pg-nix-shim.enable`.
#
# What the module does when enabled:
#   * prepends `${package}/bin` to `environment.systemPath` with `lib.mkBefore`;
#   * does NOT add the package to `environment.systemPackages` (the real `nix`
#     and `darwin-rebuild` MUST stay in /run/current-system/sw/bin, where the
#     shim's lookup finds them) and uses no `lib.hiPrio`;
#   * writes /etc/pg-nix-shim.json (`rootEndpoint`, and `verbs` when non-empty)
#     through `environment.etc."pg-nix-shim.json".text`;
#   * asserts, at evaluation, the two properties a wrong setup would break.
#
# Open item resolved against the pinned nix-darwin (nix-darwin-26.05):
#   * `environment.systemPath` is a list option (`listOf (either path str)`)
#     whose `apply` joins it with ":" (derivations as "${p}/bin") into ONE
#     STRING, so the final order is observable only as that string. Its
#     contributions merge by module-system priority order: the profile bin
#     directories (`$HOME/.nix-profile/bin:/run/current-system/sw/bin:...`) sit
#     at the default order 1000 and the system dirs at 1200. `lib.mkBefore`
#     (order 500) therefore lands before `$HOME/.nix-profile/bin` and before
#     /run/current-system/sw/bin. The generated PATH is
#     `system.build.setEnvironment` (`export PATH=${environment.systemPath}`).
#   * `environment.etc.<name>` ALWAYS yields store symlinks: `system.build.etc`
#     runs `ln -s <source> <target>`, activation links /etc/<name> to
#     /etc/static/<name>, and `text` is realised with `pkgs.writeText` into
#     /nix/store. A copy can arise only if `source` is overridden to a path
#     outside the store, or the entry is disabled; assertion (b) rejects both.
#     This module therefore uses `text` (not a user-supplied `source`), the
#     option that guarantees a store regular file behind the symlink, which is
#     what the root shim requires.
#
# Assertion (a) inspects the effective `environment.systemPath` string, the
# concrete mechanism available here: the shim directory MUST appear before
# /run/current-system/sw/bin, otherwise every `nix` call would exit 127 (the
# shim finds no real nix). There is no fallback to `systemPackages`.
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
    mkBefore
    types
    ;
  cfg = config.phillipgreenii.pg-nix-shim;

  etcName = "pg-nix-shim.json";
  systemSw = "/run/current-system/sw/bin";
  shimBin = "${cfg.package}/bin";

  pathEntries = lib.splitString ":" (toString config.environment.systemPath);
  indexOf =
    x: xs:
    let
      go =
        i:
        if i >= builtins.length xs then
          null
        else if builtins.elemAt xs i == x then
          i
        else
          go (i + 1);
    in
    go 0;
  shimIdx = indexOf shimBin pathEntries;
  swIdx = indexOf systemSw pathEntries;

  etcEntry = config.environment.etc.${etcName} or null;
  etcIsStoreSymlink =
    etcEntry != null
    && etcEntry.enable
    && lib.hasPrefix "${builtins.storeDir}/" (toString etcEntry.source);
in
{
  options.phillipgreenii.pg-nix-shim = {
    enable = mkEnableOption "the pg-nix-shim `nix` and `darwin-rebuild` wrappers first on the system PATH";
    package = mkPackageOption pkgs "pg-nix-shim" { };
    rootEndpoint = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "http://127.0.0.1:4318";
      description = ''
        OTLP/HTTP endpoint the shim passes when it runs as root, written to
        /etc/pg-nix-shim.json. Null omits the key, and a root shim then passes
        through unobserved. The machine repo SHOULD set it from the same value
        as the home-manager `pn.telemetry.endpoint`.
      '';
    };
    extraVerbs = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = ''
        Extra `nix` verbs for the root shim to wrap, written to
        /etc/pg-nix-shim.json as `verbs` only when non-empty.
      '';
    };
  };

  config = mkIf cfg.enable {
    environment.systemPath = mkBefore [ shimBin ];

    environment.etc.${etcName}.text = builtins.toJSON (
      lib.optionalAttrs (cfg.rootEndpoint != null) { inherit (cfg) rootEndpoint; }
      // lib.optionalAttrs (cfg.extraVerbs != [ ]) { verbs = cfg.extraVerbs; }
    );

    assertions = [
      {
        assertion = shimIdx != null && (swIdx == null || shimIdx < swIdx);
        message = ''
          phillipgreenii.pg-nix-shim: the generated environment.systemPath does not list the shim directory (${shimBin}) before ${systemSw}. With that order every `nix` call would exit 127 (the shim would find no real nix). The module adds it with lib.mkBefore and deliberately has no fallback to environment.systemPackages; remove the conflicting environment.systemPath override. Effective PATH: ${toString config.environment.systemPath}
        '';
      }
      {
        assertion = etcIsStoreSymlink;
        message = ''
          phillipgreenii.pg-nix-shim: environment.etc."${etcName}" is not a symlink into ${builtins.storeDir} (it is disabled or its source is outside the store). The root shim refuses any config that does not resolve to a regular file under ${builtins.storeDir}/; do not override its source or disable it.
        '';
      }
    ];
  };
}
