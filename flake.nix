{
  description = "Shared Nix infrastructure: bash-builders, dev-env helpers, module helpers, CI workflows";

  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" ];
    extra-trusted-public-keys = [
      "niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-25.11-darwin";
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/master";
    llm-agents.url = "github:numtide/llm-agents.nix";
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    nix-vscode-extensions.inputs.nixpkgs.follows = "nixpkgs";
    flake-utils.url = "github:numtide/flake-utils";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-unstable,
      llm-agents,
      nix-vscode-extensions,
      flake-utils,
      git-hooks,
      treefmt-nix,
    }:
    let
      devEnvLib = import ./nix/dev-env.nix {
        inherit nixpkgs treefmt-nix git-hooks;
      };
      moduleHelpers = import ./nix/module-helpers.nix { };
      packagesLib = import ./nix/packages.nix { };
    in
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        treefmtEval = treefmt-nix.lib.evalModule pkgs ./treefmt.nix;
        checks-lib = import ./nix/checks.nix { inherit pkgs; };
        pre-commit = devEnvLib.mkPreCommitHooks {
          inherit system;
          src = ./.;
          treefmtWrapper = treefmtEval.config.build.wrapper;
        };

        bashBuilders = packagesLib.mkBashBuilders {
          inherit pkgs self;
          inherit (pkgs) lib;
        };

        pnScripts = import ./modules/pn/scripts.nix {
          inherit pkgs bashBuilders;
        };
      in
      {
        formatter = treefmtEval.config.build.wrapper;

        packages = {
          # Test package exposing the full pn script suite check
          test-pn-scripts = pnScripts.check;
          # Individual pn scripts (available via nix shell)
          pn-discover-workspace = pnScripts.pn-discover-workspace.script;
          pn-osx-tcc-check = pnScripts.pn-osx-tcc-check.script;
          pn-workspace-init = pnScripts.pn-workspace-init.script;
          pn-workspace-apply = pnScripts.pn-workspace-apply.script;
          pn-workspace-build = pnScripts.pn-workspace-build.script;
          pn-workspace-check = pnScripts.pn-workspace-check.script;
          pn-workspace-push = pnScripts.pn-workspace-push.script;
          pn-workspace-rebase = pnScripts.pn-workspace-rebase.script;
          pn-workspace-status = pnScripts.pn-workspace-status.script;
          pn-workspace-update = pnScripts.pn-workspace-update.script;
          pn-workspace-upgrade = pnScripts.pn-workspace-upgrade.script;
          pn-store-audit = pnScripts.pn-store-audit.script;
          pn-store-deepclean = pnScripts.pn-store-deepclean.script;
        };

        apps = {
          pn-workspace-apply = {
            type = "app";
            program = "${pnScripts.pn-workspace-apply.script}/bin/pn-workspace-apply";
          };
          pn-workspace-build = {
            type = "app";
            program = "${pnScripts.pn-workspace-build.script}/bin/pn-workspace-build";
          };
        };

        checks = {
          formatting = treefmtEval.config.build.check self;
          linting = checks-lib.linting ./.;
          shellcheck = checks-lib.shellcheck {
            scripts = [
              ./lib/scripts/update-locks-lib.bash
              ./lib/scripts/update-cache-lib.bash
            ];
          };
          test-update-locks-lib = checks-lib.testUpdateLocksLib { };
        }
        // pnScripts.checks;

        devShells.default = devEnvLib.mkDevShell {
          inherit pkgs;
          pre-commit-shellHook = pre-commit.shellHook;
        };
      }
    )
    // {
      homeModules.pn = import ./home/pn/default.nix;

      lib =
        # Version helpers
        (import ./lib/version.nix)
        # Bash builders framework + package helpers
        // {
          inherit (packagesLib) mkBashBuilders mkManPage;
        }
        # Development environment helpers
        // {
          inherit (devEnvLib) mkTreefmtConfig mkPreCommitHooks mkDevShell;
        }
        # Module generation helpers
        // {
          inherit (moduleHelpers)
            mkSimplePackageModule
            mkEnableablePackageModule
            mkDockRegistration
            mkProgramModule
            ;
        }
        // {
          # Check helpers — returns attrset of check functions for a given pkgs
          mkChecks = pkgs: import ./nix/checks.nix { inherit pkgs; };

          # Overlay factories
          mkUnstableOverlay = _final: prev: {
            unstable = import nixpkgs-unstable {
              inherit (prev.stdenv.hostPlatform) system;
              config.allowUnfree = true;
            };
          };

          mkLlmAgentsOverlay = _final: prev: {
            llm-agentsPkgs = llm-agents.packages.${prev.stdenv.hostPlatform.system};
          };

          mkVscodeExtensionsOverlay = _final: prev: {
            inherit (nix-vscode-extensions.extensions.${prev.stdenv.hostPlatform.system})
              vscode-marketplace
              open-vsx
              ;
          };
        };
    };
}
