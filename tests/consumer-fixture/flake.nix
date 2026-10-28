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
        # pre-commit transitively imports treefmt
        phillipgreenii-nix-base.flakeModules.pre-commit
        phillipgreenii-nix-base.flakeModules.devshell
        phillipgreenii-nix-base.flakeModules.checks
        phillipgreenii-nix-base.flakeModules.gomod2nix-overlay
        phillipgreenii-nix-base.flakeModules.unstable-overlay
        phillipgreenii-nix-base.flakeModules.llm-agents-overlay
        phillipgreenii-nix-base.flakeModules.vscode-extensions-overlay
        phillipgreenii-nix-base.flakeModules.flox-overlay
      ];

      # phillipgreenii.src / phillipgreenii.pre-commit.src default to inputs.self;
      # consumers do not need to set them unless they want subdirectory scoping.

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

            # Bead pg2-y1z3fk: against the REAL flox input, the default
            # (floxPkgs) is flox's own packages output, and the opt-in fallback
            # (floxPkgsNoChecks, the doCheck = false chain) still evaluates to a
            # different flox derivation with nix's check phases off. Eval only:
            # booleans are computed in Nix, so the check derivation does not
            # depend on (or build) flox.
            flox-overlay =
              let
                ov = self.overlays.flox pkgs pkgs;
                own = inputs.flox.packages.${system}.flox;
                results = {
                  defaultIsFloxPackagesOutput = ov.floxPkgs.flox.drvPath == own.drvPath;
                  fallbackIsDistinctDerivation = ov.floxPkgsNoChecks.flox.drvPath != own.drvPath;
                  fallbackDisablesNixChecks =
                    ov.floxPkgsNoChecks.nix.doCheck == false && ov.floxPkgsNoChecks.nix.doInstallCheck == false;
                };
                failures = builtins.attrNames (pkgs.lib.filterAttrs (_: v: v != true) results);
              in
              pkgs.runCommand "fixture-flox-overlay" { } (
                if failures == [ ] then
                  "touch $out"
                else
                  throw "flox-overlay assertions failed: ${toString failures}"
              );

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
