{
  description = "Shared Nix infrastructure: bash-builders, dev-env helpers, module helpers, CI workflows";

  nixConfig = {
    extra-substituters = [
      "https://cache.numtide.com"
      "https://cache.flox.dev"
    ];
    extra-trusted-public-keys = [
      "niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g="
      "flox-cache-public-1:7F4OyH7ZCnFhcze3fJdfyXYLQw/aV7GEed86nQ7IsOs="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/master";
    llm-agents.url = "github:numtide/llm-agents.nix";
    flox.url = "github:flox/flox";
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
      flox,
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

        ulScripts = import ./modules/ul/scripts.nix {
          inherit pkgs bashBuilders;
          inherit (self.packages.${system}) update-locks-lib;
        };
      in
      {
        formatter = treefmtEval.config.build.wrapper;

        packages = {
          # Packaged shared bash lib. Consumed by determine-ul-lib-dir and
          # referenced via flake input by external consumers of update-locks tooling.
          update-locks-lib = pkgs.runCommand "update-locks-lib" { } ''
            mkdir -p $out/lib/scripts
            cp ${./lib/scripts/update-locks-lib.bash} $out/lib/scripts/update-locks-lib.bash
            cp ${./lib/scripts/update-cache-lib.bash} $out/lib/scripts/update-cache-lib.bash
          '';

          # Autofix helper
          fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
            ${pkgs.lib.getExe pkgs.statix} fix ${./.}
          '';

          # Install pre-commit hooks
          install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
            ${pre-commit.shellHook}
            echo "Pre-commit hooks installed successfully!"
            echo "Run 'pre-commit run --all-files' to test them."
          '';

          # Update-locks resolver
          determine-ul-lib-dir = ulScripts.determine-ul-lib-dir.script;

          # pn Go binary (single tool replacing the former pn-* bash scripts).
          pn = pkgs.callPackage ./modules/pn { inherit self; };
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

          # Pure-function unit tests for lib/version.nix:mkVersion.
          version-lib =
            let
              failures = pkgs.lib.runTests (import ./lib/version-tests.nix);
            in
            pkgs.runCommand "check-version-lib" { } (
              if failures == [ ] then
                "touch $out"
              else
                "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
            );

          # Rev-independence check: same src at two different self.rev values
          # must produce the same script drvPath. See ADR 0006.
          bash-version-rev-independent = import ./lib/bash-builders-version-tests.nix { inherit pkgs; };

          # Go test suite for pn. buildGoModule runs `go test ./...` during
          # the check phase, so building the package is equivalent to running
          # the tests. Exposing it as a check ensures `nix flake check`
          # exercises the Go tests.
          pn-go-tests = pkgs.callPackage ./modules/pn { inherit self; };

          # Hermetically verify the pn darwin module registers logSources.pn.
          pn-logsources-registration =
            let
              eval = pkgs.lib.evalModules {
                modules = [
                  # Narrow stub: declares just enough of the support-apps observability
                  # surface for the pn module to type-check standalone (the real option
                  # lives in phillipgreenii-nix-support-apps). Mirrors that flake's
                  # crossFlakeOptionStubs.
                  {
                    options.phillipgreenii.observability = {
                      enable = pkgs.lib.mkEnableOption "observability (stub)";
                      logSources = pkgs.lib.mkOption {
                        type = pkgs.lib.types.attrsOf pkgs.lib.types.anything;
                        default = { };
                      };
                    };
                    config.phillipgreenii.observability.enable = true;
                  }
                  ./darwin/modules/pn/default.nix
                ];
              };
            in
            pkgs.runCommand "pn-logsources-registration" { } (
              if eval.config.phillipgreenii.observability.logSources ? pn then
                "touch $out"
              else
                throw "pn darwin module did not register logSources.pn"
            );
        }
        // ulScripts.checks;

        devShells.default = devEnvLib.mkDevShell {
          inherit pkgs;
          pre-commit-shellHook = pre-commit.shellHook;
        };
      }
    )
    // {
      homeModules.pn = import ./home/pn/default.nix;
      # repo-base's first darwin module: registers phillipgreenii.observability.logSources.pn
      # so pn's JSONL event stream is collected into Loki (pull/filelog). Inert until a
      # machine flake imports it; see darwin/modules/pn/default.nix.
      darwinModules.pn = import ./darwin/modules/pn/default.nix;
      homeModules.install-metadata = (import ./lib/version.nix).mkInstallMetadata {
        flakeSelf = self;
        name = "phillipg-nix-repo-base";
      };

      # Single default overlay for this flake's own packages. Surfaces the pn
      # workspace tool as pkgs.pn so consumers (and homeModules.pn) consume it
      # like any other package via mkPackageOption, instead of injecting it
      # through _module.args. Mirrors overlays.default in the overlay /
      # support-apps flakes. Add future base packages here.
      overlays.default = final: _prev: {
        inherit (self.packages.${final.stdenv.hostPlatform.system}) pn;
      };

      lib =
        # Version helpers
        (import ./lib/version.nix)
        # Bash builders framework + package helpers
        // {
          inherit (packagesLib) mkBashBuilders mkGoBuilders mkManPage;
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

          mkFloxOverlay = _final: prev: {
            floxPkgs = flox.packages.${prev.stdenv.hostPlatform.system};
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
