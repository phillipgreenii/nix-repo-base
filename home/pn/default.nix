# PN home-manager module
#
# Provides pn-* workspace management scripts.
# Workspace root is discovered at runtime (walk up CWD for pn-workspace.toml).
# Apply command and hooks live in pn-workspace.toml, not here.
{
  config,
  lib,
  pkgs,
  mkBashBuildersFor,
  ...
}:
with lib;
let
  cfg = config.phillipgreenii.pn;

  bashBuilders = mkBashBuildersFor pkgs;

  pnScripts = import ../../modules/pn/scripts.nix {
    inherit pkgs bashBuilders;
  };

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
    enable = mkEnableOption "pn-* personal-nix workspace scripts";

    store.searchDirs = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = "Directories to search for Nix project roots in pn-store-audit and pn-store-deepclean. If empty, scripts default to $HOME.";
    };
  };

  config = mkIf cfg.enable {
    home.packages = pnScripts.packages;

    # Install store config only when searchDirs is non-empty
    home.file = mkIf (cfg.store.searchDirs != [ ]) {
      ".config/pn/store.toml".source = storeToml;
    };
  };
}
