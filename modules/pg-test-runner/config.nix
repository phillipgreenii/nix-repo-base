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
  # projectConcurrency (pg2-nnf7z): how many projects ONE pg-test-runner run
  # (one commit or pre-land hook run) starts at the same time. 1 = one after the
  # other, the historical behaviour and the default: raising it is opt-in because
  # only the owner of a repo knows its projects share no state (a port, a fixed
  # path, a database). maxConcurrentRuns above still caps the real parallelism
  # host-wide, so a value above it only queues. Output is buffered per project
  # and replayed in order. Override per run with
  # PG_TEST_RUNNER_PROJECT_CONCURRENCY.
  projectConcurrency = 1;
  # Per-project cap overrides (pg2-x86sp): project path suffix -> seconds. A key
  # matches a project directory when it equals the path or is a trailing run of
  # its path components (no leading/trailing "/"); longest match wins. Every
  # project NOT named here keeps the global timeoutSeconds above — the default
  # is deliberately not raised.
  #
  # The map is EMPTY on purpose. Six projects once carried a 900s override
  # (claude-extended-tool-approver, modules/pn, pg-desk, pg-router,
  # pg-connector, pg-router-handlers) because their unit suites held
  # multi-process, load and real-git tests. Operator ruling (Phillip,
  # 2026-11-11): "900s is silly large for unit tests. it should be lower and we
  # should identify slow tests and resolve them." The slow tests were then
  # tiered behind build tags or made cheap, and each suite was re-measured on
  # 2026-11-22 as `nice -n 10 go test -p 4 -race -timeout 900s ./...` (warm
  # build cache, host 1m load average 6.6-10.1, every package ok). The rule:
  # remove the override when the loaded run is at most a third of the 300s
  # default (100s, about 3x headroom); otherwise keep a lowered cap with a
  # pointer to the open work. All six qualified:
  #   packages/claude-extended-tool-approver  59s  -> removed
  #   modules/pn                              35s  -> removed
  #   packages/pg-desk                        74s  -> removed
  #   packages/pg-router                      65s  -> removed
  #   packages/pg-connector                   98s  -> removed (closest to the
  #       100s limit: if a loaded run ever times out, cut this suite's cost or
  #       add a documented cap here, never 900s by default)
  #   packages/pg-router-handlers             n/a  -> removed (a dead key: that
  #       directory has no go.mod; its tests run inside the pg-router module,
  #       measured above)
  # Re-raising any cap MUST be a visible diff: the flake check
  # `pg-test-runner-config-project-timeouts` pins the exact map.
  projectTimeouts = { };
  # Per-project extra test inputs (pg2-41fzsf): project path suffix -> globs,
  # relative to the project root, of files that make --files mode (the commit
  # hook) run that project's suite although they are not source/module/testdata
  # files. A key matches like projectTimeouts. Use it for a file a test reads
  # that no `go:embed` names (a fixture outside testdata/, a golden file read by
  # path). `*` crosses `/`; a pattern naming a directory covers everything below.
  projectExtraPaths = { };
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
      # Which changed files make --files mode (the commit hook) run a Go
      # project's suite (pg2-41fzsf): any other changed file under the project
      # (docs, YAML, README, grafana rules) skips it with a [SKIP] line. Sources
      # incl. cgo/asm inputs, module files, testdata/ and vendor/ trees, and the
      # files a `//go:embed` directive names. A file the rules cannot place, or
      # an unreadable embed directive, runs the suite. --all and path modes never
      # skip.
      inputs = {
        names = [
          "*.go"
          "go.mod"
          "go.sum"
          "go.work"
          "go.work.sum"
          "*.s"
          "*.S"
          "*.c"
          "*.h"
          "*.cc"
          "*.cpp"
          "*.cxx"
          "*.hh"
          "*.hpp"
          "*.m"
          "*.f"
          "*.syso"
          "*.swig"
          "*.swigcxx"
        ];
        dirs = [
          "testdata"
          "vendor"
        ];
        goEmbed = true;
        # --files mode runs only the packages that hold a changed .go or
        # testdata/ file plus every package of the module that imports them
        # (transitively, test imports included); go.mod, go.sum, go.work,
        # vendor/, embedded and projectExtraPaths files still run the module.
        goPackages = true;
      };
      # Per-language run environment (optional `env` object, string values),
      # set on the test process for THIS language only. A Go test process
      # otherwise sizes itself to every host CPU and all of host memory: the
      # 2026-11-12 overload had each `go test` run 11 compile jobs plus 11
      # GC workers. -p / -parallel (below) bound the build and the per-package
      # test parallelism; GOMAXPROCS bounds the runtime threads of each test
      # binary and GOMEMLIMIT makes its GC work harder before it grows. They
      # live here, not in a global environment, so they never throttle a Go
      # daemon, and absent for every non-Go language.
      env = {
        GOMAXPROCS = "4";
        GOMEMLIMIT = "2GiB";
      };
      run = {
        unit = [
          "go"
          "test"
          # {race} renders as -race unless PG_TEST_RUNNER_RACE=0 (the
          # commit-time hook), so races surface at pre-land; see
          # pg-test-runner.md "Race detector policy".
          "{race}"
          "-p"
          "4"
          "-parallel"
          "4"
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
          "-p"
          "4"
          "-parallel"
          "4"
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
          "-p"
          "4"
          "-parallel"
          "4"
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
