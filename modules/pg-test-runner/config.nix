# Default configuration registry for pg-test-runner (spec section 2.1, schema
# version 1). This is the machine-wide default: languages, run templates,
# ignore prefixes, nonUnitLabels. A single nix VALUE (not a derivation) so
# every consumer renders it the SAME way rather than restating it:
#   - the base `packages.pg-test-runner` bakes it in directly as its built-in
#     default (via mkBashScript's `config` injection), so the raw package is
#     immediately usable without any home-manager wrapping;
#   - the home-manager module (home/pg-test-runner) exposes it as the default
#     value of `phillipgreenii.pg-test-runner.config`, so a machine can extend
#     it (e.g. add repo-specific `ignore` entries) without forking the schema.
#
# Design doc: docs/superpowers/specs/2026-08-24-pg-test-runner-design.md
# (rev 6), section 2.1 (schema) and section 2.4 (per-language table). The
# `nonUnitLabels` set intentionally matches the spec's illustrated default
# exactly (`integration`, `smoke`, `contract`, `hostile`) — whether
# `verification`/`property` belong here is an OPEN classification question
# tracked by bead pg2-zdmes (support-apps), which registers its own decision
# either here or in a repo-specific `--config` once made. This file MUST NOT
# be edited to preempt that decision.
{
  version = 1;
  jobs = 0; # 0 = resolve to CPU count at run time
  timeoutSeconds = 300;
  # Host-wide run limits (pg2-r9ly8). Many agent sessions share this machine
  # and every commit-time / pre-land hook run starts a `go test -race ./...`
  # that alone wants all cores; 2-5 at once plus nix builds drove the 1m load
  # average to 50-150 on 11 cores (2026-10-07 router health review).
  #   maxConcurrentRuns: at most this many project invocations run at once
  #     across ALL pg-test-runner processes on the host (0 = unlimited). 2 is a
  #     deliberately conservative middle: one run can use the whole machine, so
  #     a bound of 1 would serialise unrelated repos, while the observed 5+
  #     overloads it. Raise it on a bigger machine.
  #   slotWaitSeconds: how long a run queues for a slot before running anyway
  #     with a warning (fail open; a hook must never block forever). Queue time
  #     never counts against timeoutSeconds. Kept under a typical 10 minute
  #     tool-call cap.
  #   niceLevel: runs execute under `nice -n <level>` (0 = off, max 19) so
  #     interactive work and daemons keep the CPU.
  # Override per run with PG_TEST_RUNNER_MAX_CONCURRENT_RUNS,
  # PG_TEST_RUNNER_SLOT_WAIT_SECONDS and PG_TEST_RUNNER_NICE_LEVEL; the lock
  # root defaults to /tmp/pg-test-runner-slots.<uid> (PG_TEST_RUNNER_LOCK_ROOT).
  maxConcurrentRuns = 2;
  slotWaitSeconds = 600;
  niceLevel = 10;
  # Per-project cap overrides (pg2-x86sp): project path suffix -> seconds. A key
  # matches a project directory when it equals the path or is a trailing run of
  # its path components (no leading/trailing "/"); longest match wins. Every
  # project NOT named here keeps the global timeoutSeconds above — the default
  # is deliberately not raised.
  #
  # packages/claude-extended-tool-approver (phillipgreenii-nix-agent-support):
  # its `go test -race ./...` suite is minutes of work on an idle machine
  # (per-package 8-55s in one measured run) and blew through the 300s cap on
  # two pre-land runs and one commit-time run under load average 19-85. The
  # tests are NOT skipped or weakened; the cap is 3x the default so a loaded
  # machine still passes while a genuinely hung suite is still killed.
  #
  # modules/daily-focus/df-survey (phillipg-nix-ziprecruiter; pg2-csfvi): its
  # bats suite (147-155 tests) needs ~390s on an idle machine and 447s under
  # load (pg2-rzr08 evidence, 2026-10-07) against the 300s default, so any
  # branch touching it timed out the pre-land hook. The tests are NOT skipped
  # or weakened; 900s is ~2x the loaded run so a loaded machine still passes
  # while a genuinely hung suite is still killed. (Operator ruling, Phillip,
  # 2026-10-08: "ok, raise the cap.")
  #
  # modules/pn (pg2-fvejk): its Go suite (internal/workspace alone is ~250s)
  # hit the 300s cap on 3 of 4 pre-commit/pre-land runs under load average
  # 23-187 (observed 2026-10-09 landing pg2-y59kf), every listed package
  # passing. The tests are NOT skipped or weakened; 900s is a ~2x+ loaded run
  # so a loaded machine passes while a genuinely hung suite is still killed.
  # (900 is the orchestrator's default by the df-survey precedent; the bead
  # left the value operator-chosen, so it is reversible.)
  projectTimeouts = {
    "packages/claude-extended-tool-approver" = 900;
    "modules/daily-focus/df-survey" = 900;
    "modules/pn" = 900;
  };
  ignore = [
    ".git/"
    ".worktrees/"
    ".direnv/"
    "_sources/"
    "node_modules/"
    "dist/"
    ".venv/"
    "fixtures/"
    "testdata/"
  ];
  nonUnitLabels = [
    "integration"
    "smoke"
    "contract"
    "hostile"
  ];
  languages = [
    {
      name = "go";
      markers = [ "go.mod" ];
      tools = [ "go" ];
      run = {
        unit = [
          "go"
          "test"
          "-race"
          "-run"
          "^(Test|Example)"
          "./..."
        ];
        # {labels} is a single comma-joined value here (not {label}): Go
        # `-tags a,b` is already OR/union semantics at the build-tag level, so
        # one invocation covers every requested label (section 2.4).
        labels = [
          "go"
          "test"
          "-tags"
          "{labels}"
          "./..."
        ];
        # Go's build-tag mechanism is a SUPERSET (tagged files ADD to the
        # default unit build, never replace it), so `all` must enumerate every
        # registered non-unit tag explicitly via {allLabels} — an unfiltered
        # `go test ./...` would silently omit every tagged file (section 2.4).
        all = [
          "go"
          "test"
          "-tags"
          "{allLabels}"
          "./..."
        ];
      };
    }
    {
      name = "python";
      markers = [ "pyproject.toml" ];
      tools = [ "uv" ];
      # pd-schedule-manager's pre-existing tests/contracts (plural) directory
      # diverges from the `contract` label name; labelAliases maps the
      # requested label to the on-disk directory name (section 2.1).
      labelAliases = {
        contract = "contracts";
      };
      run = {
        probe = {
          unit = [
            "test"
            "-d"
            "tests/unit"
          ];
        };
        unit = [
          "uv"
          "run"
          "--frozen"
          "--offline"
          "pytest"
          "tests/unit"
        ];
        # Directory IS the label (section 2.4); {label} repeats the invocation
        # once per requested label so a union request runs every directory
        # (a single pytest invocation over a comma-joined path list is not
        # portable the way {label} repetition is).
        labels = [
          "uv"
          "run"
          "--frozen"
          "--offline"
          "pytest"
          "tests/{label}"
        ];
        all = [
          "uv"
          "run"
          "--frozen"
          "--offline"
          "pytest"
          "tests"
        ];
      };
    }
    {
      name = "js";
      markers = [ "package.json" ];
      tools = [
        "npm"
        "jq"
      ];
      run = {
        probe = {
          unit = [
            "jq"
            "-e"
            ''.scripts["test:unit"]''
            "package.json"
          ];
        };
        unit = [
          "npm"
          "run"
          "test:unit"
        ];
        labels = [
          "npm"
          "run"
          "test:{label}"
        ];
        # Assumes each project's bare `test` script is non-watch (the same
        # assumption `test:unit` makes per project convention); pg-test-runner
        # does not police this — see spec 2.4's per-language caveats.
        all = [
          "npm"
          "test"
        ];
      };
    }
    {
      name = "bats";
      markers = [ "tests/*.bats" ];
      tools = [
        "bats"
        "parallel"
      ];
      labelPrefix = "type:";
      run = {
        # {jobs} always on for the unit tier (section 2.4); {unitExclusion} is
        # the negation list DERIVED from nonUnitLabels + labelPrefix, never a
        # hardcoded string.
        unit = [
          "bats"
          "--jobs"
          "{jobs}"
          "--filter-tags"
          "{unitExclusion}"
          "tests/"
        ];
        # {label} repeats once per requested label: a single --filter-tags
        # value is an AND, so a union request needs one invocation per label.
        labels = [
          "bats"
          "--filter-tags"
          "{label}"
          "tests/"
        ];
        # Genuinely unfiltered (section 2.4) — every test runs regardless of
        # tag.
        all = [
          "bats"
          "tests/"
        ];
      };
    }
  ];
}
