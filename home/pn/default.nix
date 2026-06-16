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
# (`time`/`level`/`msg`). The sibling `darwinModules.pn`
# (`darwin/modules/pn/default.nix`) registers `phillipgreenii.observability.logSources.pn`
# so the file is collected into Loki; the default glob (`${env:XDG_STATE_HOME}/pn/*.jsonl`)
# matches it, so no path override is needed. That registration is inert until a
# machine flake imports `repo-base.darwinModules.pn`.
{
  config,
  lib,
  pkgs,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.pn;

  storeToml = pkgs.writeText "pn-store.toml" (
    "search_dirs = ["
    + (concatStringsSep ", " (map (d: ''"${d}"'') cfg.store.searchDirs))
    + ''
      ]
      keep_days = 14
      keep_count = 3
    ''
  );
in
{
  options.phillipgreenii.pn = {
    enable = mkEnableOption "pn personal-nix workspace tool";

    package = mkPackageOption pkgs "pn" { };

    store.searchDirs = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = "Directories to search for Nix project roots in pn store-audit and pn store-deepclean. If empty, the tool defaults to $HOME.";
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # Install store config only when searchDirs is non-empty
    home.file = mkIf (cfg.store.searchDirs != [ ]) {
      ".config/pn/store.toml".source = storeToml;
    };
  };
}
