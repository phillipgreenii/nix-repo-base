# Light-upstream module: closes over the producer's own treefmt-nix input.
# Consumers import this module; they do NOT need to declare treefmt-nix
# themselves (it appears as a transitive node in their lock).
producerInputs:
{ lib, flake-parts-lib, ... }:
{
  imports = [ producerInputs.treefmt-nix.flakeModule ];

  perSystem = { config, pkgs, ... }: {
    treefmt = {
      projectRootFile = "flake.nix";
      programs = {
        nixfmt = {
          enable = true;
          package = pkgs.nixfmt;
        };
        prettier = {
          enable = true;
          includes = [
            "*.md"
            "*.yaml"
            "*.yml"
            "*.json"
          ];
        };
        shellcheck.enable = true;
        shfmt = {
          enable = true;
          indent_size = 2;
        };
      };
    };
  };
}
