# Reusable development environment helpers
# Provides mkTreefmtConfig, mkPreCommitHooks, mkDevShell
{
  nixpkgs,
  treefmt-nix,
  git-hooks,
}:
{
  # Create a standard treefmt configuration
  # Usage: mkTreefmtConfig { pkgs, extraPrograms ? {} }
  # Returns: treefmt-nix evalModule result
  mkTreefmtConfig =
    {
      pkgs,
      extraPrograms ? { },
    }:
    treefmt-nix.lib.evalModule pkgs {
      projectRootFile = "flake.nix";
      programs = {
        nixfmt = {
          enable = true;
          package = pkgs.nixfmt-rfc-style;
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
        shfmt = {
          enable = true;
          indent_size = 2;
        };
      }
      // extraPrograms;
    };

  # Create standard pre-commit hooks configuration
  # Usage: mkPreCommitHooks { system, src, treefmtWrapper, extraHooks ? {} }
  # Returns: git-hooks.lib.${system}.run result
  mkPreCommitHooks =
    {
      system,
      src,
      treefmtWrapper,
      extraHooks ? { },
    }:
    git-hooks.lib.${system}.run {
      inherit src;
      package = nixpkgs.legacyPackages.${system}.prek;
      tools.dotnet-sdk = nixpkgs.legacyPackages.${system}.runCommand "dotnet-stub" { } ''
        mkdir $out
      '';
      hooks = {
        treefmt = {
          enable = true;
          package = treefmtWrapper;
        };
        statix = {
          enable = true;
          name = "statix";
        };
        deadnix = {
          enable = true;
          name = "deadnix";
        };
        shellcheck = {
          enable = true;
          name = "shellcheck";
          args = [ "--severity=error" ];
        };
        check-merge-conflicts.enable = true;
        trailing-whitespace = {
          enable = true;
          entry = "${
            nixpkgs.legacyPackages.${system}.python3Packages.pre-commit-hooks
          }/bin/trailing-whitespace-fixer";
        };
        end-of-file-fixer = {
          enable = true;
          entry = "${
            nixpkgs.legacyPackages.${system}.python3Packages.pre-commit-hooks
          }/bin/end-of-file-fixer";
        };
        check-case-conflicts.enable = true;
      }
      // extraHooks;
    };

  # Create standard development shell
  # Usage: mkDevShell { pkgs, pre-commit-shellHook, extraInputs ? [] }
  # Returns: pkgs.mkShell derivation
  mkDevShell =
    {
      pkgs,
      pre-commit-shellHook,
      extraInputs ? [ ],
    }:
    pkgs.mkShell {
      shellHook = pre-commit-shellHook;
      buildInputs = [
        pkgs.nixfmt-rfc-style
        pkgs.statix
        pkgs.deadnix
        pkgs.shellcheck
      ]
      ++ extraInputs;
    };
}
