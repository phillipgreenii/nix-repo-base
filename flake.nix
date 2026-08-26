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
    # uv2nix ecosystem (ADR 0022 spike, bead pg2-r4cfy) — lock-driven Python builds.
    pyproject-nix = {
      url = "github:pyproject-nix/pyproject.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    uv2nix = {
      url = "github:pyproject-nix/uv2nix";
      inputs = {
        pyproject-nix.follows = "pyproject-nix";
        nixpkgs.follows = "nixpkgs";
      };
    };
    pyproject-build-systems = {
      url = "github:pyproject-nix/build-system-pkgs";
      inputs = {
        pyproject-nix.follows = "pyproject-nix";
        uv2nix.follows = "uv2nix";
        nixpkgs.follows = "nixpkgs";
      };
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

      # prek rewiring (design spec
      # docs/superpowers/specs/2026-08-24-pg-test-runner-design.md, section 3;
      # bead pg2-lxz3o, workstream 5). This REPLACES the former
      # `golangci-lint-prepush` hook (bead pg2-9xo3), which ran `nix build`
      # from a `pre-push` git hook -- exactly what the amended HK-2 ruling
      # (spec section 5; see also the rewritten NOTE in
      # flake-modules/pre-commit.nix) now forbids, regardless of stage. The
      # hermetic Go-lint checks that hook reused (checks.pn-golangci /
      # checks.pjira-golangci, via lib/go-builders.nix `mkGoLint`) are
      # untouched by this removal -- only the git-hook wrapper around them is
      # gone; they remain reachable via `nix flake check` / `nix build`.
      #
      # Function form (pkgs -> hooks) so the runner-presence probe below
      # follows the committing machine's own PATH/system, mirroring the
      # removed hook's own rationale.
      phillipgreenii.pre-commit.extraHooks = pkgs: {
        run-unit-tests = {
          enable = true;
          name = "unit tests (changed projects)";
          # entry is a small wrapper script (spec section 3): inside the
          # sandboxed `checks.pre-commit` derivation -- detected via the
          # SANCTIONED positive indicator NIX_BUILD_TOP (ADR 0024: "nix sets
          # it only inside a builder"; the design spec also names
          # IN_NIX_BUILD, which does not appear anywhere in this repo's
          # conventions as an actual nix-set variable -- modules/pn and ADR
          # 0024 both key on NIX_BUILD_TOP alone -- so it is checked too as a
          # harmless no-op belt-and-suspenders, but NIX_BUILD_TOP is what
          # actually fires) -- the hermetic checks.* tier already covers
          # every project's unit tests directly against source there, so
          # this prints a skip notice and exits 0 rather than re-running
          # them. Outside the sandbox it execs pg-test-runner against this
          # repo's OWN rendered config, threaded via the
          # packages.pg-test-runner-repo-config export below -- the exact
          # same pgTestRunnerRepoConfigJson derivation the
          # pg-test-runner-repo-config-ignores-bash-builders-tests check
          # exercises, referenced rather than re-rendered here. The runner
          # itself is resolved from the committer's PATH via `command -v`,
          # never hardcoded to a /nix/store path: a prek hook's `entry` runs
          # in the committer's actual shell PATH, not through this flake's
          # own `pkgs`, and only the CONFIG is a static per-repo artifact
          # safe to pin via Nix interpolation (spec section 3). A MISSING
          # pg-test-runner MUST fail loudly, never silently skip -- an
          # absence-keyed skip would let any PATH breakage no-op the only
          # commit-time test gate, which this workspace treats as hook
          # bypassing.
          entry = "${pkgs.writeShellScript "run-unit-tests" ''
            set -eo pipefail
            if [[ -n "$IN_NIX_BUILD" || -n "$NIX_BUILD_TOP" ]]; then
              echo "run-unit-tests: inside the nix sandbox; skipping (checks.* already covers this)"
              exit 0
            fi
            if ! command -v pg-test-runner >/dev/null 2>&1; then
              echo "run-unit-tests: pg-test-runner not found on PATH -- provision it via the HM profile before committing (see docs/superpowers/specs/2026-08-24-pg-test-runner-design.md)" >&2
              exit 11
            fi
            exec pg-test-runner --config ${
              self.packages.${pkgs.stdenv.hostPlatform.system}.pg-test-runner-repo-config
            } --labels unit --files "$@"
          ''}";
          pass_filenames = true;
          require_serial = true;
        };
      };

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
          # pnwf (workforest work-cycle) module: the shared pnwf-lib
          # primitives plus the `pnwf` command itself — all subcommands
          # (resolve/repos/stage/fork-preflight/land-plan/cleanup/status/
          # sync-fetch) are implemented (bead pg2-xs5cj) — plus `wsplan`, the
          # read-only Stage A land-plan emitter, a SECOND independent command
          # in the same module (bead pg2-wjt8k.3).
          #
          # `pn` is threaded in from self.packages (mirroring ulScripts just
          # above), because `wsplan` shells `pn workspace info --json` for the
          # set directory. It MUST NOT come off `pkgs`: nixpkgs has no `pn`
          # attribute, and overlays.default — the only thing that surfaces one
          # — is exported for consumers and never applied to this flake's own
          # pkgs. There is no recursion hazard: packages.pn does not depend on
          # pnwfScripts, and nix attrset values are lazy per attribute.
          pnwfScripts = import ./modules/pnwf/scripts.nix {
            inherit pkgs bashBuilders;
            inherit (self.packages.${system}) pn;
          };
          # pg-go-mutate (Go mutation-testing diagnostic) module: the shared
          # pg-go-mutate-lib primitives plus the `pg-go-mutate` command itself.
          # No extra threaded package (unlike pnwfScripts' `pn`): the engine
          # (pkgs.phillipgreenii.gomu) is bound only in the home-manager module,
          # at wrap time in a CONSUMER's pkgs — never here. This flake's own
          # pkgs applies only overlays.gomod2nix, and overlays.default (the
          # only thing that would surface phillipgreenii.gomu) is exported for
          # consumers and never applied to this flake's own pkgs.
          pgGoMutateScripts = import ./modules/pg-go-mutate/scripts.nix {
            inherit pkgs bashBuilders;
          };
          # pg-test-runner (label-driven direct test runner) module: the
          # data-driven engine + its default configuration registry (spec
          # section 2.1). No extra threaded package -- it resolves every
          # language tool from PATH only (section 2.5).
          pgTestRunnerScripts = import ./modules/pg-test-runner/scripts.nix {
            inherit pkgs bashBuilders;
          };
          # This repo's OWN repo-specific pg-test-runner configuration: the
          # shared default registry (modules/pg-test-runner/config.nix) plus
          # an `ignore` entry for lib/bash-builders-tests/ -- the mkBashBuilders
          # framework's own bats-suite fixtures (sample-cmd/, sample-lib/, ...),
          # whose tests/*.bats exist to be driven by lib/bash-builders-tests's
          # own check derivations (specific env vars, SCRIPTS_DIR/LIB_PATH
          # wiring), never to be run standalone by pg-test-runner as if they
          # were ordinary unit-tested projects. Not yet threaded into a prek
          # hook (workstream 5, a separate bead) -- this is the config
          # artifact + regression check that workstream depends on, plus
          # proof it actually keeps pg-test-runner off that directory (bead
          # pg2-jcqar acceptance criterion 4).
          pgTestRunnerRepoConfig = (import ./modules/pg-test-runner/config.nix) // {
            ignore = (import ./modules/pg-test-runner/config.nix).ignore ++ [
              "lib/bash-builders-tests/"
            ];
          };
          pgTestRunnerRepoConfigJson =
            (pkgs.formats.json { }).generate "pg-test-runner-repo-config.json"
              pgTestRunnerRepoConfig;
          # Go builders (mkGoApp / mkGoBinary / mkGoLint) over the gomod2nix engine.
          goBuilders = import ./lib/go-builders.nix { inherit pkgs self; };
          # Pattern-B (local `replace => ../sibling`) fixture source shared by the
          # go-builders-patternb-* checks below. base ships no Pattern-B module of
          # its own, so this dep-free fixture is what gives base's OWN flake check
          # coverage of the local-replace path that mkGoLint/mkGoTest's modRoot
          # forwarding depends on (bead pg2-sjxhy). Rooted at the parent so the
          # `replace => ../modb` sibling lives in the same store tree, mirroring the
          # real Pattern-B packages (e.g. agent-support's ccpool).
          goPatternBFixtureSrc = lib.fileset.toSource {
            root = ./lib/tests/fixtures/patternb;
            fileset = lib.fileset.unions [
              ./lib/tests/fixtures/patternb/moda
              ./lib/tests/fixtures/patternb/modb
            ];
          };
        in
        {
          _module.args.pkgs = import inputs.nixpkgs {
            inherit system;
            overlays = [ self.overlays.gomod2nix ];
          };

          # Pinned bats + parallel in the devShell so the local bats test-loop
          # (e.g. the pnwf suites, `bats --jobs 8 tests/`) runs from these pinned
          # deps instead of a cold `nix run nixpkgs#bats` re-eval on every run, and
          # so `bats --jobs` has the GNU parallel it requires (bead pg2-nh1t3).
          phillipgreenii.devshell.extraInputs = [
            pkgs.bats
            pkgs.parallel
          ];

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

            # pnwf: deterministic helper for the workforest work-cycle.
            pnwf = pnwfScripts.pnwf.script;

            # wsplan: read-only land-plan emitter (Stage A of `land`). This
            # export is what makes it a real public command: flake.nix consumes
            # only pnwfScripts.checks plus explicit per-package attrs — never
            # pnwfScripts.packages — so without it the command reaches nobody.
            wsplan = pnwfScripts.wsplan.script;

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

            # pg-go-mutate: reports which assertions a Go package's tests are
            # missing (bash CLI wrapping the pinned gomu mutation engine).
            # Mirrors how pnwf/wsplan expose their module's script above.
            pg-go-mutate = pgGoMutateScripts.pg-go-mutate.script;

            # pg-go-mutate-tui: interactive, file-granular resumable
            # mutation-testing TUI Go binary -- the sole orchestrator for
            # unattended/multi-package mutation sweeps.
            pg-go-mutate-tui = pkgs.callPackage ./modules/pg-go-mutate/pg-go-mutate-tui { inherit self; };

            # pg-test-runner: label-driven, nix-free-at-runtime direct test
            # runner (spec docs/superpowers/specs/2026-08-24-pg-test-runner-design.md).
            pg-test-runner = pgTestRunnerScripts.pg-test-runner.script;

            # This repo's own rendered pg-test-runner config (pgTestRunnerRepoConfig
            # above), exposed as a package SOLELY so the `run-unit-tests` prek hook
            # (phillipgreenii.pre-commit.extraHooks, top-level in this file) can
            # reference this EXACT derivation via `self.packages.${system}` --
            # extraHooks is a top-level (non-perSystem) option, so it cannot see
            # this `let` binding directly. Also exercised by the
            # pg-test-runner-repo-config-ignores-bash-builders-tests check below.
            pg-test-runner-repo-config = pgTestRunnerRepoConfigJson;

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
                      ./capability-model
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

            # Regression guard for the pnwf runner agents' turn-boundary contract
            # (bead pg2-es5nn). `pnwf-update-runner` once started the long
            # `pnwf update-relock --set` step with `run_in_background` and then
            # ENDED ITS TURN "waiting for the notification" — but only the MAIN
            # session survives to receive one, so the job was torn down mid-relock:
            # no strict-JSON status line, and a half-relocked set whose own
            # cleanliness pre-flight then REFUSED the re-run. Both runner
            # definitions therefore carry the "One Turn, Foreground Only" rule
            # (R1-R4), the `600000` ms foreground ceiling, and an incomplete-*
            # halt reason. Nothing else validates these markdown definitions
            # (treefmt only formats them), so assert the load-bearing text is
            # present AND that no path re-permits backgrounding: every
            # `run_in_background` mention MUST sit on a prohibition line.
            pnwf-runner-turn-boundary-rule = pkgs.runCommand "check-pnwf-runner-turn-boundary-rule" { } ''
              set -euo pipefail
              update_runner=${./pn-workspace-rules/agents/pnwf-update-runner.md}
              sync_runner=${./pn-workspace-rules/agents/pnwf-runner.md}
              update_cmd=${./pn-workspace-rules/commands/pn-workspace-update.md}
              sync_cmd=${./pn-workspace-rules/commands/pn-workspace-sync.md}

              for f in "$update_runner" "$sync_runner"; do
                for needle in \
                  'Constraint: One Turn, Foreground Only' \
                  '**R1**' '**R2**' '**R3**' '**R4**' \
                  '600000'; do
                  grep -qF -- "$needle" "$f" || {
                    echo "$f: missing required text: $needle" >&2
                    exit 1
                  }
                done

                # Every run_in_background mention in a RUNNER must be a
                # prohibition. Judged over a 3-line window (the mention plus the
                # two lines above it) so a reflow cannot fail this spuriously
                # while a genuinely permissive sentence still trips it.
                awk -v file="$f" '
                  /run_in_background/ {
                    if ((prev2 " " prev1 " " $0) !~ /MUST NOT/) {
                      printf "%s:%d: %s\n", file, FNR, $0 > "/dev/stderr"
                      bad = 1
                    }
                  }
                  { prev2 = prev1; prev1 = $0 }
                  END { exit bad ? 1 : 0 }
                ' "$f" || {
                  echo "$f: run_in_background named outside a MUST NOT prohibition (see above) — a subagent cannot await its own background job across the end of its turn" >&2
                  exit 1
                }
              done

              grep -qF -- 'incomplete-update' "$update_runner" || {
                echo "pnwf-update-runner.md: missing the incomplete-update halt reason" >&2
                exit 1
              }
              grep -qF -- 'incomplete-sync' "$sync_runner" || {
                echo "pnwf-runner.md: missing the incomplete-sync halt reason" >&2
                exit 1
              }

              # The dispatch brief is a named contributing factor: each command
              # must WITHHOLD the background option from its runner.
              for f in "$update_cmd" "$sync_cmd"; do
                grep -qF -- 'MUST NOT offer the runner `run_in_background`' "$f" || {
                  echo "$f: dispatch step no longer withholds run_in_background from the runner" >&2
                  exit 1
                }
              done

              touch $out
            '';

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

            # mkGoBinary's arg set is CLOSED and its mkGoApp call explicit, so
            # `baseVersion`, `ldflags` and `env` were unreachable from a
            # mkGoBinary consumer entirely — a release build could never carry a
            # real semver, and a consumer's `-s -w` / `CGO_ENABLED=0` were
            # SILENTLY dropped (bead tc-5lxy.26). This check proves all three now
            # reach the build, on a REAL build rather than by eyeballing eval.
            #
            # It is NOT an ADR 0006 exception: assertion 1 pins the version to
            # `2.1.0-<8hex>`, i.e. the per-source digest is STILL appended, so a
            # bare `2.1.0` (or any repo-git-rev version) fails the check.
            #
            # Baselines that make the last two assertions differential rather
            # than vacuous, measured on this repo's own `pn` (which passes
            # neither knob -- probe its `bin/.pn-wrapped`, since `bin/pn` is the
            # bash wrapper): `go version -m` reports `CGO_ENABLED=1`, and
            # `go tool nm` reports 8496 DEFINED symbols including `main.main`,
            # against 0 and none for the stripped probe.
            #
            # EVERY ASSERTION HERE MUST BE PLATFORM-PORTABLE, AND `file` OUTPUT
            # IS NOT (bd pg2-lpkxm). This check originally asserted that `file`
            # said "statically linked" (for `CGO_ENABLED=0`) and ", stripped"
            # (for `ldflags`), baselined against Linux `file` output. On
            # aarch64-darwin the probe's `file` line is
            # `Mach-O 64-bit executable arm64` (`pkgs.file` in the sandbox spells
            # it `Mach-O 64-bit arm64 executable, flags:<|DYLDLINK|PIE>`) and so
            # contains NEITHER string: the first assertion failed and the second
            # would have failed immediately after it, breaking `nix flake check`
            # for the whole repo on this system. The linkage assertion is also
            # UNSATISFIABLE on darwin rather than merely mismeasured -- there is
            # no static libSystem, so a Go binary is always dynamically linked
            # there whatever `CGO_ENABLED` is -- and it was redundant besides:
            # assertion 3 below reads Go's OWN recorded build setting, which is
            # authoritative on every platform and differential against the `pn`
            # baseline. So it is gone, and MUST NOT be reintroduced. The
            # `ldflags` assertion is NOT redundant (`-s -w` is not recorded in
            # the build settings), so it is kept and asked portably.
            #
            # `nm` is the wrong tool for that and MUST NOT be substituted for
            # `go tool nm`: on a stripped Mach-O it still lists the 58 undefined
            # dynamic-linker imports that `-s -w` does not remove, so it reports
            # symbols for a binary that has no symbol table.
            go-builders-binary-passthrough =
              let
                probe = goBuilders.mkGoBinary {
                  # Must match the installed exe name: `subPackages = [ "." ]`
                  # on module `example.com/goversion` installs
                  # $out/bin/goversion, and mkGoBinary's postInstall + meta
                  # address $out/bin/<name>.
                  name = "goversion";
                  src = ./lib/tests/fixtures/goversion;
                  gomod2nixToml = ./lib/tests/fixtures/goversion/gomod2nix.toml;
                  subPackages = [ "." ];
                  baseVersion = "2.1.0";
                  ldflags = [ "-s -w" ];
                  env.CGO_ENABLED = 0;
                  # The man page / completion half of mkGoBinary is covered by
                  # go-builders-completions-merge; disabling it here keeps this
                  # check about the three forwarded arguments (and keeps the
                  # fixture free of a CLI framework).
                  manPage = false;
                  completions = {
                    bash = false;
                    zsh = false;
                    fish = false;
                  };
                };
              in
              pkgs.runCommand "check-go-builders-binary-passthrough"
                {
                  # `pkgs.file` is deliberately NOT here: every assertion below
                  # asks Go's own tooling, for the portability reason in the
                  # header. Re-adding it is the signal that a `file`-output
                  # assertion is creeping back in.
                  nativeBuildInputs = [ pkgs.go ];
                  inherit probe;
                  inherit (probe) version;
                }
                ''
                  set -euo pipefail
                  bin="$probe/bin/goversion"

                  # 1. baseVersion is honored AND the ADR 0006 source digest is
                  #    still appended.
                  echo "derivation version: $version"
                  printf '%s' "$version" | grep -Eq '^2\.1\.0-[0-9a-f]{8}$' || {
                    echo "FAIL: derivation version '$version' does not match ^2\.1\.0-[0-9a-f]{8}\$ (baseVersion not forwarded, or the digest was dropped)" >&2
                    exit 1
                  }

                  # 2. that same string reached the linker: the binary prints
                  #    main.Version, which mkGoApp injects with -X.
                  runtime="$("$bin")"
                  echo "runtime version: $runtime"
                  [ "$runtime" = "$version" ] || {
                    echo "FAIL: binary reports '$runtime' but the derivation version is '$version'" >&2
                    exit 1
                  }

                  # 3/4. Go's embedded build settings.
                  export HOME="$TMPDIR"
                  buildinfo="$(go version -m "$bin")"
                  printf '%s\n' "$buildinfo"
                  printf '%s\n' "$buildinfo" | grep -q 'CGO_ENABLED=0' || {
                    echo "FAIL: env.CGO_ENABLED = 0 did not reach the build (pn's baseline is CGO_ENABLED=1)" >&2
                    exit 1
                  }

                  # 5. `-s -w` is not recorded in Go's build settings, so the
                  #    absence of the symbol table is the observable proof the
                  #    caller's ldflags reached the linker. `go tool nm` lists
                  #    DEFINED symbols only for a binary that still has one, so
                  #    `main.main` -- which every Go program defines, and which
                  #    assertion 2 just proved is present at LINK time by running
                  #    the -X-injected version -- is absent exactly when `-s`
                  #    applied. See the header for why this is not `file` or `nm`.
                  echo "defined symbols: $(go tool nm "$bin" | grep -cv ' U ' || true)"
                  if go tool nm "$bin" | grep -q 'main\.main'; then
                    echo "FAIL: binary retains the main.main symbol, so ldflags = [ \"-s -w\" ] were dropped" >&2
                    exit 1
                  fi

                  touch $out
                '';

            # Python builder checks (uv2nix, ADR 0022). Each test file imports the
            # builder DIRECTLY and so MUST receive the 3 uv2nix inputs (the outer
            # currying stage) or `nix flake check` fails at eval.

            # AC1 (ADR 0011): the nvd-visible derivation version is 0.0.0-<digest>,
            # stamped on the wrapper. Eval-only (does not force loadWorkspace).
            python-version-digest = import ./lib/python-package-version-tests.nix {
              inherit pkgs;
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };

            # D1 headline proof: the shipped closure equals uv.lock, not incidental
            # nixpkgs versions (six pinned 1.16.0 vs nixpkgs 1.17.0). Also asserts
            # the relocated runtime version stamp took (AC2/AC3 Tier-1 slice).
            python-lock-version-drift = import ./lib/python-package-drift-tests.nix {
              inherit pkgs;
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };

            # Lock-driven resolution (beads pg2-gjwpl -> pg2-r4cfy): a dep absent
            # from nixpkgs by name (eventsourcing) resolves from uv.lock and
            # imports. The fail-loud NEGATIVE is deferred to the Tier-2/3 follow-up
            # (see the test file header).
            python-resolve-lock-driven = import ./lib/python-package-resolve-tests.nix {
              inherit pkgs;
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };

            # D2: the agent-support shape (instantiate factory, no app/lock) still
            # evaluates after currying.
            python-factory-currying-eval = import ./lib/python-factory-currying-tests.nix {
              inherit pkgs;
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };

            # sdist build-path (ADR 0022, bead pg2-abgyl): a wheel-less dep
            # (termcolor==1.1.0) forces uv2nix through the sdist build +
            # pyproject-build-systems overlay — the path the wheel-shipping
            # fixtures (six/eventsourcing) never exercise. Builds + imports it.
            python-sdist-build-path = import ./lib/python-package-sdist-tests.nix {
              inherit pkgs;
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };

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

            # Smoke (end-to-end scenario) gate for pn: the SAME `go test ./...` as
            # pn-go-tests but with `-tags smoke`, which is the only way the
            # internal/workspace/smoke suite compiles or runs at all.
            #
            # Why this check exists (bead pg2-nuacd): the suite is behind
            # `//go:build smoke`, so plain `go test ./...` and every other flake
            # check silently EXCLUDED it. Nothing compiled it, let alone ran it —
            # and a build tag that nothing ever sets is indistinguishable from
            # deleted code. Measured consequence: the TOFU hook trust gate landed
            # 2026-07-12 (cd30f10) and broke four scenarios (S18/S19/S29/S36),
            # which then "passed by never running" for a month; separately S33/S33b
            # kept asserting `update` pushes to the remote — the exact opposite of
            # the contract ADR 0023 shipped. A test suite that cannot fail is not a
            # gate. This check makes a wrong assertion in the suite fail
            # `nix flake check` — verified by injecting one deliberately. Note the
            # golangci-lint-prepush hook above builds only the two *-golangci
            # checks, so it does NOT run this one; `nix flake check` / CI is the
            # gate. golangci-lint also does not lint the smoke files at all, since
            # .golangci.yml sets no build tags — the `go vet` that mkGoTest runs as
            # `go test`'s default (ADR 0021) is what covers them.
            #
            # It is a SEPARATE check rather than `-tags smoke` folded into
            # pn-go-tests so the smoke suite's much larger environmental surface
            # (it builds the pn binary, forks bash, drives real git over file://
            # bare remotes) cannot take the primary unit-test gate down with it, and
            # so the gate is DISCOVERABLE by name in `nix flake show`. The overlap
            # is the module's non-smoke suite running twice (~11s) — cheap next to
            # the alternative of it running never.
            #
            # Dependencies, established empirically rather than assumed: git (real
            # clones/commits/pushes, but only to file:// bare remotes — never the
            # network), bash, and nix. nix is NOT optional here: `workspace lock`
            # evaluates each repo's flake inputs, so dropping pkgs.nix fails ~23
            # scenarios, not just the one nix-marked scenario. Local nix evaluation
            # works fine inside this sandbox; what does NOT is fetching an EXTERNAL
            # flake input. Exactly one scenario needs that — S23 (`nix fmt` on a
            # generated flake with a `nixpkgs` input) — and it self-skips here via
            # its `requires-network` marker (see smoke_test.go's networkAvailable
            # probe), so it still runs for a developer outside the sandbox.
            #
            # No scenario invokes darwin-rebuild, sudo, or any real activation:
            # `apply` runs the synthetic apply_command from the scenario's own
            # pn-workspace.toml, so S19/S36 never reach the real machine.
            pn-smoke-tests = goBuilders.mkGoTest {
              pname = "pn-smoke";
              src = ./modules/pn;
              gomod2nixToml = ./modules/pn/gomod2nix.toml;
              testDeps = [
                pkgs.git
                pkgs.nix
                pkgs.bash
              ];
              testFlags = [
                "-tags"
                "smoke"
                "-count=1"
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

            # Pattern-B regression guard for the Go builders (bead pg2-sjxhy).
            # base has no Pattern-B (local `replace`) module of its own, so
            # mkGoLint/mkGoTest's modRoot forwarding would otherwise be validated
            # only downstream. These three checks exercise the local-replace path
            # in base's OWN flake check via the lib/tests/fixtures/patternb fixture
            # (moda imports sibling modb through `replace => ../modb`, so the
            # builder must cd into modRoot="moda" with modb resolved alongside).
            # They are RED before the modRoot fix — golangci-lint / go test run at
            # the fileset root, which has no go.mod ("directory prefix . does not
            # contain main module") — and GREEN after. The -build check pins
            # mkGoApp (already correct) so the whole builder family stays covered.
            go-builders-patternb-lint = goBuilders.mkGoLint {
              pname = "patternb-fixture";
              src = goPatternBFixtureSrc;
              modRoot = "moda";
              gomod2nixToml = ./lib/tests/fixtures/patternb/moda/gomod2nix.toml;
              config = ./.golangci.yml;
            };
            go-builders-patternb-test = goBuilders.mkGoTest {
              pname = "patternb-fixture";
              src = goPatternBFixtureSrc;
              modRoot = "moda";
              gomod2nixToml = ./lib/tests/fixtures/patternb/moda/gomod2nix.toml;
            };
            go-builders-patternb-build = goBuilders.mkGoApp {
              pname = "patternb-fixture";
              src = goPatternBFixtureSrc;
              modRoot = "moda";
              gomod2nixToml = ./lib/tests/fixtures/patternb/moda/gomod2nix.toml;
              subPackages = [ "." ];
            };

            # Hermetically verify the exported darwinModules.default (the aggregate
            # the machine actually imports) registers logSources.pn, and --
            # since pg-go-mutate-tui's darwin module now shares this same
            # aggregate -- its logSources/metricsTargets/alertRuleFiles too.
            pn-logsources-registration =
              let
                eval = pkgs.lib.evalModules {
                  modules = [
                    # Narrow stub: declares just enough of the support-apps observability
                    # surface for the pn and pg-go-mutate-tui modules to type-check
                    # standalone (the real options live in phillipgreenii-nix-support-apps).
                    # Mirrors that flake's crossFlakeOptionStubs.
                    {
                      options.phillipgreenii.observability = {
                        enable = pkgs.lib.mkEnableOption "observability (stub)";
                        logSources = pkgs.lib.mkOption {
                          type = pkgs.lib.types.attrsOf pkgs.lib.types.anything;
                          default = { };
                        };
                        metricsTargets = pkgs.lib.mkOption {
                          type = pkgs.lib.types.attrsOf (
                            pkgs.lib.types.submodule {
                              options.port = pkgs.lib.mkOption { type = pkgs.lib.types.port; };
                            }
                          );
                          default = { };
                        };
                        alertRuleFiles = pkgs.lib.mkOption {
                          type = pkgs.lib.types.listOf pkgs.lib.types.path;
                          default = [ ];
                        };
                      };
                      config.phillipgreenii.observability.enable = true;
                    }
                    ./darwin
                  ];
                };
                obs = eval.config.phillipgreenii.observability;
              in
              pkgs.runCommand "pn-logsources-registration" { } (
                if !(obs.logSources ? pn) then
                  throw "pn darwin module did not register logSources.pn"
                else if !(obs.logSources ? pg-go-mutate-tui) then
                  throw "pg-go-mutate-tui darwin module did not register logSources.pg-go-mutate-tui"
                else if (obs.metricsTargets.pg-go-mutate-tui or null) == null then
                  throw "pg-go-mutate-tui darwin module did not register metricsTargets.pg-go-mutate-tui"
                else if obs.metricsTargets.pg-go-mutate-tui.port != 9464 then
                  throw "pg-go-mutate-tui darwin module registered the wrong metrics port"
                else if obs.alertRuleFiles == [ ] then
                  throw "pg-go-mutate-tui darwin module did not register an alertRuleFiles entry"
                else
                  "touch $out"
              );

            # Regression guard: alerting.yaml's rules MUST set noDataState: OK
            # explicitly (an idle period between TUI sessions is normal, not a
            # paging condition).
            pg-go-mutate-tui-alerting-nodata = pkgs.runCommand "pg-go-mutate-tui-alerting-nodata" { } ''
              if grep -q 'noDataState: OK' ${./modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml}; then
                touch $out
              else
                echo "modules/pg-go-mutate/pg-go-mutate-tui/alerting.yaml is missing 'noDataState: OK'" >&2
                exit 1
              fi
            '';

            # Hermetically verify home/pg-go-mutate-tui/default.nix actually
            # renders its `settings` option to a config.json file, and that
            # the configured keys survive into it. Narrow stub, mirroring
            # pn-logsources-registration above: this repo has no home-manager
            # flake input, so instead of a real activationPackage we declare
            # just enough of the xdg.configFile/home.packages surface for the
            # module to type-check and render standalone, then hand-realize
            # its `xdg.configFile` entries under $out/home-files -- the same
            # path a real home-manager build would produce.
            pg-go-mutate-tui-config-rendered =
              let
                eval = pkgs.lib.evalModules {
                  specialArgs = { inherit pkgs; };
                  modules = [
                    {
                      options = {
                        xdg.configHome = pkgs.lib.mkOption {
                          type = pkgs.lib.types.str;
                          default = ".config";
                        };
                        xdg.configFile = pkgs.lib.mkOption {
                          type = pkgs.lib.types.attrsOf (
                            pkgs.lib.types.submodule {
                              options.source = pkgs.lib.mkOption { type = pkgs.lib.types.path; };
                            }
                          );
                          default = { };
                        };
                        home.packages = pkgs.lib.mkOption {
                          type = pkgs.lib.types.listOf pkgs.lib.types.package;
                          default = [ ];
                        };
                      };
                    }
                    ./home/pg-go-mutate-tui/default.nix
                    {
                      config.phillipgreenii.pg-go-mutate-tui = {
                        enable = true;
                        settings.concurrency = 3;
                      };
                    }
                  ];
                };
                renderedFile = eval.config.xdg.configFile."pg-go-mutate-tui/config.json".source;
              in
              pkgs.runCommand "pg-go-mutate-tui-config-rendered" { inherit renderedFile; } ''
                mkdir -p "$out/home-files/.config/pg-go-mutate-tui"
                ln -s "$renderedFile" "$out/home-files/.config/pg-go-mutate-tui/config.json"
                cat "$out/home-files/.config/pg-go-mutate-tui/config.json"
                grep -q '"concurrency": 3' "$out/home-files/.config/pg-go-mutate-tui/config.json"
              '';

            # Eval-level check: the Light capability framework (Plan 5) behaves —
            # feature/isHuman gating, development subscription, bundle veto, and the
            # account-property typo→error guarantee.
            capability-framework-eval =
              let
                r = import ./tests/capability-framework.nix { inherit (pkgs) lib; };
                failures = builtins.attrNames (
                  pkgs.lib.filterAttrs (_: v: v == false) (removeAttrs r [ "allPass" ])
                );
              in
              pkgs.runCommand "capability-framework-eval" { } (
                if r.allPass then
                  "touch $out"
                else
                  throw "capability-framework assertions failed: ${toString failures}"
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

            # Bead pg2-jcqar acceptance criterion 4: this repo's own
            # repo-specific pg-test-runner config (pgTestRunnerRepoConfig,
            # above) MUST keep pg-test-runner from misclassifying/choking on
            # lib/bash-builders-tests/ -- the mkBashBuilders framework's own
            # bats-suite fixtures, which exist to be driven by that
            # directory's own check derivations, never run standalone. A
            # synthetic fixture tree (not the real repo) proves the ignore
            # entry is DECISIVE, differentially: the SAME binary, run with
            # vs. without the repo config, discovers lib/bash-builders-tests
            # only in the without-repo-config case, while a real project
            # (realproj) is discovered either way.
            pg-test-runner-repo-config-ignores-bash-builders-tests =
              let
                ptr = pgTestRunnerScripts.pg-test-runner.script;
                fixtureGoMod = pkgs.writeText "pg-test-runner-fixture-go.mod" ''
                  module example.com/realproj

                  go 1.21
                '';
                fixtureBats = pkgs.writeText "pg-test-runner-fixture-sample.bats" ''
                  #!/usr/bin/env bats
                  @test "framework fixture" { true; }
                '';
              in
              pkgs.runCommand "check-pg-test-runner-repo-config-ignores-bash-builders-tests"
                {
                  nativeBuildInputs = [
                    pkgs.git
                    pkgs.go
                  ];
                }
                ''
                  set -euo pipefail
                  root="$TMPDIR/fixture"
                  mkdir -p "$root"
                  cd "$root"
                  git init -q .

                  mkdir -p realproj
                  cp ${fixtureGoMod} realproj/go.mod

                  mkdir -p lib/bash-builders-tests/sample-cmd/tests
                  cp ${fixtureBats} lib/bash-builders-tests/sample-cmd/tests/test-sample-cmd.bats

                  with_repo_config="$("${ptr}/bin/pg-test-runner" --config ${pgTestRunnerRepoConfigJson} --all 2>&1)" || true
                  echo "-- with repo config --"
                  echo "$with_repo_config"
                  echo "$with_repo_config" | grep -q 'realproj' || {
                    echo "FAIL: repo config's --all did not discover the real project" >&2
                    exit 1
                  }
                  if echo "$with_repo_config" | grep -q 'bash-builders-tests'; then
                    echo "FAIL: repo config's --all still discovered lib/bash-builders-tests/ (ignore entry not effective)" >&2
                    exit 1
                  fi

                  with_default_config="$("${ptr}/bin/pg-test-runner" --all 2>&1)" || true
                  echo "-- with bare default config (no repo override) --"
                  echo "$with_default_config"
                  echo "$with_default_config" | grep -q 'bash-builders-tests' || {
                    echo "FAIL: expected the bare default config to still discover lib/bash-builders-tests/, proving the repo config's ignore entry -- not fixture structure -- is what excludes it" >&2
                    exit 1
                  }

                  touch $out
                '';
          }
          // ulScripts.checks
          // pnwfScripts.checks
          // pgGoMutateScripts.checks
          // pgTestRunnerScripts.checks
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
          pg-go-mutate = import ./home/pg-go-mutate/default.nix;
          pg-go-mutate-tui = import ./home/pg-go-mutate-tui/default.nix;
          pg-test-runner = import ./home/pg-test-runner/default.nix;
          install-metadata = ./home-modules/install-metadata.nix;
          # Light capability model framework (Plan 5): declares the shared
          # phillipgreenii.account.* property namespace + phillipgreenii.bundles.*
          # aggregation options. Installs nothing; capability DEFINITIONS live in
          # the consuming flakes. Import alongside those capability modules.
          # Exported as a PATH (not `import`ed) so the module system keys it by path
          # and DEDUPES when both nix-personal (accounts resolver threads it) and
          # nix-agent-support (homeModules.capabilities imports it) pull it into the
          # same home-manager eval — otherwise the shared account.* options are
          # declared twice ("already declared"). Mirrors install-metadata above.
          capability-framework = ./home/capability-framework/default.nix;
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
            pg-go-mutate
            pg-go-mutate-tui
            pg-test-runner
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
          # Python package builder factory (lock-driven via uv2nix, ADR 0022;
          # per-source digest versioning retained, ADR 0011). The uv2nix ecosystem
          # inputs are curried in HERE — the loader is per-package (needs each
          # package's src) so it cannot be a global pkgs overlay like gomod2nix;
          # the exported factory keeps its `{ pkgs; lib; mkSrcDigest; }` signature.
          // {
            mkPythonBuilders = import ./lib/python-package.nix {
              inherit (inputs) uv2nix pyproject-nix pyproject-build-systems;
            };
          }
          # Activation-script output helpers
          // (import ./lib/activation.nix { })
          # Capability-authoring helpers for the Light capability model (Plan 5):
          # mkCapability / mkBundle / enableFeatureIf.
          // (import ./lib/capabilities.nix { lib = inputs.nixpkgs.lib; });
      };
    };
}
