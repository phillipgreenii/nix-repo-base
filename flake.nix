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
    flake-parts.url = "github:hercules-ci/flake-parts";
    flake-parts.inputs.nixpkgs-lib.follows = "nixpkgs";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
    gomod2nix = {
      url = "github:nix-community/gomod2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{
      self,
      flake-parts,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [
        (import ./flake-modules/pre-commit.nix inputs)
        ./flake-modules/devshell.nix
        ./flake-modules/checks.nix
        (import ./flake-modules/overlays/gomod2nix.nix inputs)
      ];

      systems = [
        "x86_64-linux"
        "aarch64-darwin"
      ];

      # phillipgreenii.{src, pre-commit.src} default to inputs.self via the
      # checks and pre-commit modules; no explicit settings needed here.

      perSystem =
        {
          pkgs,
          system,
          checksHelpers,
          ...
        }:
        let
          inherit (pkgs) lib;
          inherit ((import ./nix/packages.nix { })) mkClaudeMarketplaceBuilders;
          bashBuilders = (import ./nix/packages.nix { }).mkBashBuilders {
            inherit pkgs self;
            inherit (pkgs) lib;
          };
          ulScripts = import ./modules/ul/scripts.nix {
            inherit pkgs bashBuilders;
            inherit (self.packages.${system}) update-locks-lib;
          };
        in
        {
          _module.args.pkgs = import inputs.nixpkgs {
            inherit system;
            overlays = [ self.overlays.gomod2nix ];
          };

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

            # Update-locks resolver
            determine-ul-lib-dir = ulScripts.determine-ul-lib-dir.script;

            # pn Go binary (single tool replacing the former pn-* bash scripts).
            pn = pkgs.callPackage ./modules/pn { inherit self; };

            # This repo's own Claude Code marketplace, bundled into the store with
            # content-derived per-plugin version stamping. Identity:
            # phillipg-nix-repo-base-marketplace-local. The fileset is NARROWED to
            # just the marketplace manifest + plugin dirs (NOT ./.) to avoid closure
            # bloat and re-realize on unrelated edits. See ADR-0010 +
            # docs/claude-marketplaces.md.
            phillipg-nix-repo-base-marketplace =
              (mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace
                {
                  src = lib.fileset.toSource {
                    root = ./.;
                    fileset = lib.fileset.unions [
                      ./.claude-plugin/marketplace.json
                      ./pn-workspace-rules
                    ];
                  };
                };
          };

          checks = {
            # formatting, linting, consumer-input-alignment auto-contributed by checks module
            shellcheck = checksHelpers.shellcheck {
              scripts = [
                ./lib/scripts/update-locks-lib.bash
                ./lib/scripts/update-cache-lib.bash
              ];
            };
            test-update-locks-lib = checksHelpers.testUpdateLocksLib { };

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

            # Pure-function unit tests for lib/claude-marketplace.nix.
            claude-marketplace-lib =
              let
                failures = pkgs.lib.runTests (import ./lib/claude-marketplace-tests.nix { inherit pkgs; });
              in
              pkgs.runCommand "check-claude-marketplace-lib" { } (
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

            # Hermetically verify the exported darwinModules.default (the aggregate
            # the machine actually imports) registers logSources.pn.
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
                    ./darwin
                  ];
                };
              in
              pkgs.runCommand "pn-logsources-registration" { } (
                if eval.config.phillipgreenii.observability.logSources ? pn then
                  "touch $out"
                else
                  throw "pn darwin module did not register logSources.pn"
              );

            # Eval-time check: fixture files exist and lock declares all heavy inputs.
            consumer-fixture-eval =
              pkgs.runCommand "consumer-fixture-eval"
                {
                  nativeBuildInputs = [ pkgs.jq ];
                }
                ''
                  set -euo pipefail
                  test -f ${./tests/consumer-fixture}/flake.nix
                  test -f ${./tests/consumer-fixture}/flake.lock
                  ${pkgs.jq}/bin/jq -e '.nodes | has("nixpkgs-unstable")' ${./tests/consumer-fixture}/flake.lock >/dev/null
                  ${pkgs.jq}/bin/jq -e '.nodes | has("llm-agents")' ${./tests/consumer-fixture}/flake.lock >/dev/null
                  ${pkgs.jq}/bin/jq -e '.nodes | has("flox")' ${./tests/consumer-fixture}/flake.lock >/dev/null
                  ${pkgs.jq}/bin/jq -e '.nodes | has("nix-vscode-extensions")' ${./tests/consumer-fixture}/flake.lock >/dev/null
                  touch $out
                '';
          }
          // ulScripts.checks;
        };

      flake = {
        flakeModules = {
          treefmt = import ./flake-modules/treefmt.nix inputs;
          pre-commit = import ./flake-modules/pre-commit.nix inputs;
          devshell = ./flake-modules/devshell.nix;
          checks = ./flake-modules/checks.nix;
          gomod2nix-overlay = import ./flake-modules/overlays/gomod2nix.nix inputs;
          unstable-overlay = ./flake-modules/overlays/unstable.nix;
          llm-agents-overlay = ./flake-modules/overlays/llm-agents.nix;
          vscode-extensions-overlay = ./flake-modules/overlays/vscode-extensions.nix;
          flox-overlay = ./flake-modules/overlays/flox.nix;
        };

        homeModules.pn = import ./home/pn/default.nix;
        # repo-base's first darwin module set, exported as the aggregate
        # darwinModules.default (mirrors agent-support). Currently carries the pn
        # module, which registers phillipgreenii.observability.logSources.pn so pn's
        # JSONL event stream is collected into Loki (pull/filelog). Inert until a
        # machine flake imports it; see darwin/default.nix and darwin/modules/pn.
        darwinModules.default = ./darwin;
        homeModules.install-metadata = ./home-modules/install-metadata.nix;

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
            inherit ((import ./nix/packages.nix { }))
              mkBashBuilders
              mkGoBuilders
              mkManPage
              mkClaudeMarketplaceBuilders
              ;
          }
          # Module generation helpers
          // {
            inherit ((import ./nix/module-helpers.nix { }))
              mkSimplePackageModule
              mkEnableablePackageModule
              mkDockRegistration
              mkProgramModule
              ;
          };
      };
    };
}
