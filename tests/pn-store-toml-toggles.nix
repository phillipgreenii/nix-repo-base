# Eval-level test for the pn store.toml devbox/flox toggles. Evaluates
# home/pn/default.nix with a stubbed home-manager option surface for three
# settings (only flox, only devbox, nothing) and returns whether home.file
# installs .config/pn/store.toml in each, plus the generated file sources.
# Consumed by the pn-store-toml-toggles flake check, which reads the sources
# inside a runCommand (no import from derivation).
{ pkgs }:
let
  inherit (pkgs) lib;
  mk =
    store:
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
              phillipgreenii.pn = {
                enable = true;
                inherit store;
              };
            };
          }
        )
      ];
    }).config;
  key = ".config/pn/store.toml";
  flox = mk { flox = true; };
  devbox = mk { devbox = true; };
  none = mk { };
in
{
  floxInstalled = flox.home.file ? ${key};
  devboxInstalled = devbox.home.file ? ${key};
  noneInstalled = none.home.file ? ${key};
  floxSource = flox.home.file.${key}.source;
  devboxSource = devbox.home.file.${key}.source;
}
