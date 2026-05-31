# PN home-manager module
#
# Provides the pn workspace management binary (Go).
# Workspace root is discovered at runtime (walk up CWD for pn-workspace.toml).
# Apply command and hooks live in pn-workspace.toml, not here.
#
# The consumer is responsible for providing the `pn` package via _module.args:
#   _module.args.pn = inputs.phillipgreenii-nix-base.packages.${system}.pn;
{
  config,
  lib,
  pkgs,
  pn,
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

    store.searchDirs = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = "Directories to search for Nix project roots in pn store-audit and pn store-deepclean. If empty, the tool defaults to $HOME.";
    };
  };

  config = mkIf cfg.enable {
    home.packages = [ pn ];

    # Install store config only when searchDirs is non-empty
    home.file = mkIf (cfg.store.searchDirs != [ ]) {
      ".config/pn/store.toml".source = storeToml;
    };
  };
}
