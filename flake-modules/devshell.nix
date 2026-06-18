# No producer-input closure needed — devshell uses only the consumer's pkgs.
{ lib, config, ... }:
let
  topLevelCfg = config.phillipgreenii.devshell;
in
{
  options.phillipgreenii.devshell = {
    extraInputs = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
      description = "Additional packages added to the default devShell.";
    };
  };

  config.perSystem =
    {
      pkgs,
      preCommitShellHook ? "",
      ...
    }:
    {
      devShells.default = pkgs.mkShell {
        shellHook = preCommitShellHook;
        buildInputs = [
          pkgs.nixfmt
          pkgs.statix
          pkgs.deadnix
          pkgs.shellcheck
        ]
        ++ topLevelCfg.extraInputs;
      };
    };
}
