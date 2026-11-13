# Eval-level test for the pn-workspace.toml missing-file activation warning
# (bead tc-w1iu3). Evaluates home/pn/default.nix with a stubbed home-manager
# option surface and returns the activation script text, whether home.file
# touches pn-workspace.toml, and whether the activation exists when pn is
# disabled. Consumed by the pn-workspace-toml-warning flake check, which also
# RUNS the script.
{ pkgs }:
let
  inherit (pkgs) lib;
  mk =
    enable:
    (lib.evalModules {
      modules = [
        ../home/pn/default.nix
        (
          { lib, ... }:
          {
            options.home = {
              packages = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              file = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              activation = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
            };
            options.warnings = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              default = [ ];
            };
            config = {
              _module.args.pkgs = pkgs // {
                pg-nix-log-wrapped = pkgs.hello;
                pn = pkgs.hello;
              };
              phillipgreenii.pn.enable = enable;
            };
          }
        )
      ];
    }).config;
  on = mk true;
  off = mk false;
in
{
  inherit (on.home.activation.pnWorkspaceTomlWarn) data;
  onWritesToml = lib.any (n: lib.hasSuffix "pn-workspace.toml" n) (builtins.attrNames on.home.file);
  offHasActivation = off.home.activation ? pnWorkspaceTomlWarn;
}
