# Narrow nix-darwin stubs for evaluating darwin/modules/pg-nix-shim standalone
# (this repo has no nix-darwin flake input). They mirror the pinned nix-darwin
# (nix-darwin-26.05) definitions the module depends on:
#   * environment.systemPath: listOf (either path str), `apply` joins with ":"
#     (derivations as "${p}/bin"); profile dirs at the default order and the
#     system dirs at order 1200, as modules/environment/default.nix does;
#   * environment.etc: attrsOf submodule with enable/text/target/source where
#     source defaults to a writeText derivation (modules/lib/write-text.nix);
#   * assertions: listOf anything (modules/system/default.nix).
{ lib, pkgs }:
{
  options = {
    environment.systemPath = lib.mkOption {
      type = lib.types.listOf (lib.types.either lib.types.path lib.types.str);
      apply = x: lib.concatMapStringsSep ":" (p: if lib.isDerivation p then "${p}/bin" else p) x;
    };
    environment.etc = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule (
          { config, name, ... }:
          {
            options = {
              enable = lib.mkOption {
                type = lib.types.bool;
                default = true;
              };
              text = lib.mkOption {
                type = lib.types.lines;
                default = "";
              };
              target = lib.mkOption {
                type = lib.types.str;
                default = name;
              };
              source = lib.mkOption { type = lib.types.path; };
            };
            config.source = lib.mkDefault (pkgs.writeText "etc-${name}" config.text);
          }
        )
      );
      default = { };
    };
    assertions = lib.mkOption {
      type = lib.types.listOf lib.types.anything;
      default = [ ];
    };
  };
  config.environment.systemPath = lib.mkMerge [
    [
      "$HOME/.nix-profile/bin"
      "/run/current-system/sw/bin"
      "/nix/var/nix/profiles/default/bin"
    ]
    (lib.mkOrder 1200 [ "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" ])
  ];
}
