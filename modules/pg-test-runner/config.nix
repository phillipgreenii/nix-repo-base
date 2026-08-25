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
