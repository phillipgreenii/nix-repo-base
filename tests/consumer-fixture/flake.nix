{
  description = "Consumer fixture for phillipgreenii-nix-base producer rev verification";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    llm-agents.url = "github:numtide/llm-agents.nix";
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    flox.url = "github:flox/flox";
    flake-parts.url = "github:hercules-ci/flake-parts";
    phillipgreenii-nix-base = {
      url = "path:../..";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{
      self,
      flake-parts,
      phillipgreenii-nix-base,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "x86_64-linux"
        "aarch64-darwin"
      ];

      imports = [
        phillipgreenii-nix-base.flakeModules.treefmt
        phillipgreenii-nix-base.flakeModules.pre-commit
        phillipgreenii-nix-base.flakeModules.devshell
        phillipgreenii-nix-base.flakeModules.checks
        phillipgreenii-nix-base.flakeModules.gomod2nix-overlay
        phillipgreenii-nix-base.flakeModules.unstable-overlay
        phillipgreenii-nix-base.flakeModules.llm-agents-overlay
        phillipgreenii-nix-base.flakeModules.vscode-extensions-overlay
        phillipgreenii-nix-base.flakeModules.flox-overlay
      ];

      phillipgreenii = {
        src = ./.;
        pre-commit.src = ./.;
      };

      perSystem =
        {
          pkgs,
          system,
          ...
        }:
        {
          _module.args.pkgs = import inputs.nixpkgs {
            inherit system;
            overlays = [ self.overlays.gomod2nix ];
          };

          checks = {
            lib-bash-builders =
              let
                bashBuilders = phillipgreenii-nix-base.lib.mkBashBuilders {
                  inherit pkgs;
                  inherit (pkgs) lib;
                  inherit self;
                };
              in
              pkgs.runCommand "fixture-lib-bash-builders" { } ''
                test -n "${toString (builtins.attrNames bashBuilders)}"
                touch $out
              '';

            lib-go-builders =
              let
                goBuilders = phillipgreenii-nix-base.lib.mkGoBuilders {
                  inherit pkgs;
                  inherit (pkgs) lib;
                  self = null;
                };
              in
              pkgs.runCommand "fixture-lib-go-builders" { } ''
                test -n "${toString (builtins.attrNames goBuilders)}"
                touch $out
              '';

            lib-version-helpers = pkgs.runCommand "fixture-lib-version-helpers" { } ''
              test "${phillipgreenii-nix-base.lib.mkGitHash "abcdef1234567890"}" = "abcdef1"
              touch $out
            '';
          };
        };

      flake = {
        homeModules.install-metadata = _: {
          imports = [ phillipgreenii-nix-base.homeModules.install-metadata ];
          phillipgreenii.install-metadata = {
            flakeSelf = self;
            name = "consumer-fixture";
          };
        };
      };
    };
}
