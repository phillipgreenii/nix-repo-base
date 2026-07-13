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
          # Go builders (mkGoApp / mkGoBinary / mkGoLint) over the gomod2nix engine.
          goBuilders = import ./lib/go-builders.nix { inherit pkgs self; };
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

            # Update-locks resolver
            determine-ul-lib-dir = ulScripts.determine-ul-lib-dir.script;

            # pn Go binary (single tool replacing the former pn-* bash scripts).
            pn = pkgs.callPackage ./modules/pn { inherit self; };

            # pn-workspace-toml-enforce: a separate, internal entrypoint in the
            # same Go module as pn. It reuses internal/workspace serialization to
            # enforce the two nix-owned pn-workspace.toml keys ([workspace].id +
            # [hooks.apply].post). Consumed by phillipg-nix-ziprecruiter's
            # pn-workspace-toml home-manager activation. See docs/adr/0017.
            pn-workspace-toml-enforce = pkgs.callPackage ./modules/pn/enforce-toml.nix { inherit self; };

            # pjira Go binary (generic Atlassian Jira access tool).
            pjira = pkgs.callPackage ./modules/jira { inherit self; };

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

            # Regression guard for the allowWarnings shellcheck helper (bead
            # pg2-ncyg5, commit 31a48ab). allowWarnings raises the reporting FLOOR
            # to `error` so warnings are tolerated while genuine ERRORS still fail.
            # The helper previously appended `|| true`, which swallowed EVERY
            # finding — errors included. A planted script carrying an ERROR-level
            # finding (SC2157) is fed to checksHelpers.shellcheck with
            # allowWarnings = true; the resulting helper derivation MUST fail to
            # build. We run the helper's OWN build command in an errexit subshell
            # and INVERT its exit status, so this check passes only when the helper
            # still fails on the error — pre-fix (`|| true`) it would have "passed"
            # the planted error, failing this check. The fixture is a generated
            # store path (pkgs.writeText), NOT a tracked .sh, so the repo-wide
            # treefmt/pre-commit shellcheck never sees the deliberate error.
            shellcheck-allowwarnings-errors-not-swallowed =
              let
                plantedError = pkgs.writeText "planted-shellcheck-error.sh" ''
                  #!/usr/bin/env bash
                  # SC2157 (severity=error): argument to -n is always true due to a
                  # literal string. A genuine ERROR-level finding, not a warning.
                  if [ -n foo ]; then
                    echo always
                  fi
                '';
                helperDrv = checksHelpers.shellcheck {
                  scripts = [ plantedError ];
                  allowWarnings = true;
                };
              in
              pkgs.runCommand "check-shellcheck-allowwarnings-errors-not-swallowed" { } ''
                # Keep errexit OFF out here so we can inspect the exit code; run the
                # helper's own build command in a subshell WITH errexit so its
                # shellcheck failure aborts before the helper's trailing `touch $out`.
                set +e
                (
                  set -e
                  ${helperDrv.buildCommand}
                )
                rc=$?
                set -e
                if [ "$rc" -eq 0 ]; then
                  echo "FAIL: checksHelpers.shellcheck allowWarnings=true PASSED a script with an ERROR-level finding — errors are being swallowed (regression of bead pg2-ncyg5)" >&2
                  exit 1
                fi
                echo "OK: allowWarnings=true still FAILS on ERROR-level shellcheck findings (helper exited $rc); errors are not swallowed"
                touch $out
              '';
            test-update-locks-lib = checksHelpers.testUpdateLocksLib { };

            # Pure-function unit tests for lib/ul-pin.nix:isUnpinnedUpdateLocks,
            # the predicate behind the auto-contributed update-locks-pinned guard
            # (bead pg2-o784p). Covers detected (bare), not-detected (pinned), and
            # not-detected (base direct-source) cases.
            update-locks-pin-predicate =
              let
                failures = pkgs.lib.runTests (import ./lib/ul-pin-tests.nix { inherit (pkgs) lib; });
              in
              pkgs.runCommand "check-update-locks-pin-predicate" { } (
                if failures == [ ] then
                  "touch $out"
                else
                  "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
              );

            # Fixture test for the NRB_REV extraction jq filter that each consumer
            # update-locks.sh uses to pin the resolver (bead pg2-o784p). Asserts both
            # branches: node present -> rev; node absent -> empty (unpinned fallback).
            update-locks-nrb-rev-filter =
              pkgs.runCommand "check-update-locks-nrb-rev-filter" { nativeBuildInputs = [ pkgs.jq ]; }
                ''
                  set -euo pipefail
                  filter='.locks.nodes."phillipgreenii-nix-base".locked.rev // empty'
                  got=$(echo '{"locks":{"nodes":{"phillipgreenii-nix-base":{"locked":{"rev":"deadbeef"}}}}}' | jq -r "$filter")
                  [ "$got" = "deadbeef" ] || { echo "expected deadbeef, got '$got'" >&2; exit 1; }
                  got=$(echo '{"locks":{"nodes":{"other":{}}}}' | jq -r "$filter")
                  [ -z "$got" ] || { echo "expected empty, got '$got'" >&2; exit 1; }
                  touch $out
                '';

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

            activation-lib =
              let
                failures = pkgs.lib.runTests (import ./lib/activation-tests.nix { inherit (pkgs) lib; });
              in
              pkgs.runCommand "check-activation-lib" { } (
                if failures == [ ] then
                  "touch $out"
                else
                  "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
              );

            activation-behavior =
              let
                sectionFile = pkgs.writeText "demo-section.sh" (
                  (import ./lib/activation.nix { }).mkActivationSection {
                    tag = "demo";
                    headline = "checking";
                    body = ''
                      act_ok "all good"
                      act_warn 'careful %s \ $HOME'
                      act_fail "broke"
                      act_info "fyi"
                    '';
                  }
                );
              in
              pkgs.runCommand "check-activation-behavior" { } ''
                set -euo pipefail
                # Policy: color defaults ON; NO_COLOR is the only off-switch.
                # Even though runCommand stdout is a pipe (no TTY) and
                # CLICOLOR_FORCE is unset, color MUST be emitted because NO_COLOR
                # is unset. This is precisely what makes nix-darwin's `env -i`
                # system activation (where CLICOLOR_FORCE/TTY can never be seen)
                # come out colored.
                plain=$(LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash ${sectionFile})
                printf '%s\n' "$plain"
                if ! printf '%s' "$plain" | grep -q $'\033\[32m'; then echo "FAIL: no green by default (color must be on unless NO_COLOR)"; exit 1; fi
                if ! printf '%s' "$plain" | grep -q '✓'; then echo "FAIL: missing UTF-8 glyph"; exit 1; fi
                # Also exercise the home-manager activation envelope: home.activation
                # runs each block under `bash -eu -o pipefail`. The section must
                # behave identically there (e.g. nounset must not trip on the
                # color/glyph guards). Assert byte-identical output to the plain run.
                envelope=$(LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash -eu -o pipefail ${sectionFile})
                if [ "$envelope" != "$plain" ]; then echo "FAIL: hm activation envelope output differs"; exit 1; fi
                # CLICOLOR_FORCE is no longer consulted (color is on regardless);
                # kept as a regression guard that setting it does not break output.
                forced=$(CLICOLOR_FORCE=1 LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash ${sectionFile})
                if ! printf '%s' "$forced" | grep -q $'\033\[32m'; then echo "FAIL: no green with CLICOLOR_FORCE"; exit 1; fi
                # NO_COLOR must win over CLICOLOR_FORCE.
                nocolor=$(NO_COLOR=1 CLICOLOR_FORCE=1 LC_CTYPE=UTF-8 ${pkgs.bash}/bin/bash ${sectionFile})
                if printf '%s' "$nocolor" | grep -q $'\033'; then echo "FAIL: NO_COLOR did not win over CLICOLOR_FORCE"; exit 1; fi
                # ASCII fallback when locale is not UTF-8.
                ascii=$(LC_ALL=C LC_CTYPE=C ${pkgs.bash}/bin/bash ${sectionFile})
                if ! printf '%s' "$ascii" | grep -q '\[OK\]'; then echo "FAIL: no ASCII marker"; exit 1; fi
                if printf '%s' "$ascii" | grep -q '✓'; then echo "FAIL: glyph leaked into ASCII mode"; exit 1; fi
                # Arbitrary message stays literal (%, backslash, $).
                if ! printf '%s' "$plain" | grep -F 'careful %s \ $HOME' >/dev/null; then echo "FAIL: msg not literal"; exit 1; fi
                touch $out
              '';

            # Rev-independence check: same src at two different self.rev values
            # must produce the same script drvPath. See ADR 0006.
            bash-version-rev-independent = import ./lib/bash-builders-version-tests.nix { inherit pkgs; };

            # Forces an mkBashScript fixture's `.check` to build so `nix flake check`
            # exercises the assembled-artifact floor smoke + SCRIPT_UNDER_TEST path
            # (bead pg2-28wwb).
            bash-builders-artifact-smoke = import ./lib/bash-builders-smoke-tests.nix { inherit pkgs; };

            # Config-injection safety: metacharacter values are escaped, not
            # executed, and non-identifier keys fail at eval (pg2-92603).
            bash-config-injection = import ./lib/bash-builders-injection-tests.nix { inherit pkgs; };

            # mkGoBinary must MERGE a partial `completions` override over the
            # all-true defaults, not replace the whole attrset (bead pg2-beppe).
            # Forcing the probe derivation's drvPath forces the postInstall
            # interpolation that reads completions'.{bash,zsh,fish} — the pre-fix
            # "replace" behavior threw "attribute 'bash' missing" here — while the
            # pure merge asserts the untouched shells stay enabled.
            go-builders-completions-merge =
              let
                probe = goBuilders.mkGoBinary {
                  name = "completions-probe";
                  src = ./modules/pn;
                  gomod2nixToml = ./modules/pn/gomod2nix.toml;
                  subPackages = [ "cmd/pn" ];
                  completions = {
                    fish = false;
                  }; # partial override — bash/zsh must survive
                };
                instantiates = (builtins.tryEval probe.drvPath).success;
                merged = goBuilders.completionDefaults // {
                  fish = false;
                };
                ok = instantiates && merged.bash && merged.zsh && !merged.fish;
              in
              pkgs.runCommand "check-go-builders-completions-merge" { } (
                if ok then
                  "touch $out"
                else
                  "echo 'mkGoBinary partial completions not merged over defaults (bead pg2-beppe)' >&2; exit 1"
              );

            # mkGoApp's `version` is derived (baseVersion + digest); its open
            # `...` arg set used to SILENTLY discard a caller-passed `version`.
            # It must now throw instead (bead pg2-zvt37).
            go-builders-app-rejects-version =
              let
                rejected =
                  !(builtins.tryEval (
                    goBuilders.mkGoApp {
                      pname = "reject-version-probe";
                      src = ./modules/pn;
                      gomod2nixToml = ./modules/pn/gomod2nix.toml;
                      subPackages = [ "cmd/pn" ];
                      version = "9.9.9"; # illegal: version is derived, use baseVersion
                    }
                  )).success;
              in
              pkgs.runCommand "check-go-builders-app-rejects-version" { } (
                if rejected then
                  "touch $out"
                else
                  "echo 'mkGoApp silently accepted a caller-passed version (bead pg2-zvt37)' >&2; exit 1"
              );

            # Per-source digest in the Python derivation version (ADR 0011): the
            # mkPythonBuilders factory's mkPythonPackage must stamp 0.0.0-<digest>.
            python-version-digest = import ./lib/python-package-version-tests.nix { inherit pkgs; };

            # Fail-fast dependency resolution (bead pg2-gjwpl): an unresolved
            # pyproject dependency must throw by default, not silently drop.
            python-resolve-fail-fast =
              let
                failures = pkgs.lib.runTests (import ./lib/python-package-resolve-tests.nix { inherit pkgs; });
              in
              pkgs.runCommand "check-python-resolve-fail-fast" { } (
                if failures == [ ] then
                  "touch $out"
                else
                  "echo ${pkgs.lib.escapeShellArg (builtins.toJSON failures)} >&2; exit 1"
              );

            # Full Go test gate for pn: runs `go test ./...` UNSCOPED over the whole
            # module (cmd/* + internal/*). The pn *package* build pins
            # subPackages=[cmd/pn], which scopes gomod2nix's check hook and would
            # skip the internal/* suite — so the real test gate is a dedicated
            # mkGoTest, NOT the package build (bead pg2-2jqj0). git+nix are supplied
            # for the tests that shell out to them.
            pn-go-tests = goBuilders.mkGoTest {
              pname = "pn";
              src = ./modules/pn;
              gomod2nixToml = ./modules/pn/gomod2nix.toml;
              testDeps = [
                pkgs.git
                pkgs.nix
              ];
            };

            # Full Go test gate for pjira (explicit mkGoTest so it stays real even if
            # pjira ever grows a second cmd/* entrypoint — mirrors pn-go-tests).
            pjira-go-tests = goBuilders.mkGoTest {
              pname = "pjira";
              src = ./modules/jira;
              gomod2nixToml = ./modules/jira/gomod2nix.toml;
            };

            # golangci-lint over each Go module, run OFFLINE via gomod2nix's
            # vendored dep env so it passes in the no-network `nix flake check`
            # sandbox (bead pg2-6wly). Replaces the old network-dependent
            # golangci-lint pre-commit hook, which fetched deps from proxy.golang.org
            # and failed under sandbox=true. Both modules lint against the repo-root
            # .golangci.yml (passed explicitly — it lives outside the module src).
            pn-golangci = goBuilders.mkGoLint {
              pname = "pn";
              src = ./modules/pn;
              gomod2nixToml = ./modules/pn/gomod2nix.toml;
              config = ./.golangci.yml;
            };
            pjira-golangci = goBuilders.mkGoLint {
              pname = "pjira";
              src = ./modules/jira;
              gomod2nixToml = ./modules/jira/gomod2nix.toml;
              config = ./.golangci.yml;
            };

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
          // ulScripts.checks
          # Light the foundational bash-builder contract suite (18 bats + module-shape
          # assertion across mkBashLibrary/mkBashScript/mkBashModule). Was dead code —
          # never imported by any .nix (bead pg2-fqar3 / prior deep-dive T1).
          // (import ./lib/bash-builders-tests { inherit bashBuilders pkgs; }).checks;
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

        homeModules = {
          pn = import ./home/pn/default.nix;
          pjira = import ./home/pjira/default.nix;
          install-metadata = ./home-modules/install-metadata.nix;
        };
        # repo-base's first darwin module set, exported as the aggregate
        # darwinModules.default (mirrors agent-support). Currently carries the pn
        # module, which registers phillipgreenii.observability.logSources.pn so pn's
        # JSONL event stream is collected into Loki (pull/filelog). Inert until a
        # machine flake imports it; see darwin/default.nix and darwin/modules/pn.
        darwinModules.default = ./darwin;

        # Single default overlay for this flake's own packages. Surfaces the pn
        # workspace tool as pkgs.pn so consumers (and homeModules.pn) consume it
        # like any other package via mkPackageOption, instead of injecting it
        # through _module.args. Mirrors overlays.default in the overlay /
        # support-apps flakes. Add future base packages here.
        overlays.default = final: _prev: {
          inherit (self.packages.${final.stdenv.hostPlatform.system})
            pn
            pn-workspace-toml-enforce
            pjira
            ;
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
          # Python package builder factory (per-source digest versioning; ADR 0011)
          // {
            mkPythonBuilders = import ./lib/python-package.nix;
          }
          # Activation-script output helpers
          // (import ./lib/activation.nix { });
      };
    };
}
