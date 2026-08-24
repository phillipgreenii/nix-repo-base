# pg-test-runner — Label-Driven Direct Test Runner

- **Date:** 2026-08-24
- **Motivating context:** the workspace git-hook speed evaluation
  (`~/phillipg_mbp/docs/2026-08-24-git-hook-speed-evaluation.md`) found that all commit/push
  slowness comes from hooks that run test suites as `nix build`s of whole-suite derivations.
  Operator direction (Phillip, 2026-08-24, this design session): pre-commit hooks MUST NOT invoke
  nix; each project MUST have a way to run its unit tests directly and quickly; smoke/integration
  tests are excluded from pre-commit; nix-managed tools MUST be available without running nix
  builds per commit. This design supersedes the evaluation doc's HK-2 blanket ban for the
  label-`unit` tier (see "Relationship to the evaluation doc" below).
- **Verified feasibility (2026-08-24):** a repo-base `mkBashScript` bats suite
  (`modules/ul/determine-ul-lib-dir/tests/`) passes when run directly against source with no nix
  invocation (~0.6s CPU for 6 tests). The bash-scripting skill already states the core principle
  "tests MUST work without nix build". bats 1.12 supports `--filter-tags` with `!` negation.
  No bats file in any of the six repos carries a tag today.

## 1. Test-kind definitions (normative, all languages)

These definitions classify by STRUCTURE (what a test needs), never by a stopwatch. A reader MUST
be able to classify a test by inspection.

- **`unit`** — exercises one unit of the project: a function, class, or script-function boundary.
  Collaborators are mocked or faked to isolate the unit under test. No shared context between
  tests, therefore parallel-safe and order-independent. Expected to be very fast. Strives for high
  completeness of the unit's behavior.
- **`integration`** — exercises interaction between higher-level components. MAY use shared
  fixtures, real collaborators, or require a specific ordering.
- **`smoke`** — end-to-end sanity of the assembled artifact.
- **`contract`** — drives real external systems (live Claude/bd/git/PagerDuty and similar; costs
  tokens, credentials, or money).

A test without a label MUST be treated as `unit`. This default is a safety net, not a steady
state: every suite SHOULD carry an explicit label (workstream 2). The vocabulary above is the
COMMON set — new labeling SHOULD use it for consistency — but it is not mechanically closed: the
runner treats labels as OPAQUE strings and passes unknown labels through to the per-language
selection mechanisms (sections 2.1 and 2.4). A repo-specific label (e.g. the pre-existing Go
build tag `hostile`) therefore works without any runner change. The one obligation: every
non-unit label actually in use MUST be registered in the configuration's `nonUnitLabels` set, so
the unit tier's exclusion stays correct where the mechanism needs an enumerated negation (bats).
In Go, any REGISTERED non-unit tag disqualifies mechanically (a tagged file leaves the default
build); platform/GOOS build constraints (`//go:build darwin`, `_darwin_test.go` filenames) are
build constraints, NOT labels — a platform-gated unit test is still unit.

Parallelism is a DERIVED property: because `unit` tests have no shared context by definition, the
runner MAY always parallelize the unit tier, and MUST NOT assume parallel safety for any other
kind (non-unit kinds run serially unless a suite declares otherwise).

## 2. The runner

`pg-test-runner` is a repo-base module (`mkBashScript`-built, bats-tested), installed on PATH via
the home-manager profile. The tool is nix-managed but nix-free at runtime.

### 2.1 Configuration (nix-generated JSON)

The runner contains NO hardcoded language knowledge. It is a data-driven engine over a
configuration file — a registry of language strategies. Adding a language, changing a command, or
registering a label is a configuration change, never a code change.

- **Declared in nix, rendered to JSON.** A repo-base module option
  (`phillipgreenii.pg-test-runner.config`, an attrset) is rendered via `pkgs.formats.json` and
  baked into the installed wrapper as the default configuration — the machine-wide registry. A
  repo needing repo-specific entries renders its own JSON in nix and threads it as
  `--config <store path>` in its generated prek hook entry (resolved at hook-install time, so
  still no nix at commit time). The JSON is always generated, never hand-written.
- **Resolution precedence:** `--config <path>` flag, else `PG_TEST_RUNNER_CONFIG` env var, else
  the baked-in default.
- **Schema (version 1), illustrated with the Go entry:**

```json
{
  "version": 1,
  "jobs": 0,
  "timeoutSeconds": 300,
  "ignore": ["_sources/**", "node_modules/**", "dist/**", ".venv/**"],
  "nonUnitLabels": ["integration", "smoke", "contract", "hostile"],
  "languages": [
    {
      "name": "go",
      "markers": ["go.mod"],
      "tools": ["go"],
      "run": {
        "unit": ["go", "test", "-race", "-run", "^(Test|Example)", "./..."],
        "labels": ["go", "test", "-tags", "{labels}", "./..."],
        "all": ["go", "test", "-tags", "{allLabels}", "./..."]
      }
    }
  ]
}
```

- `jobs` — parallelism width substituted for `{jobs}`; `0` means the CPU count.
- `timeoutSeconds` — wall-clock cap per project invocation; expiry reports as a test failure
  (exit `10`) naming the project and the cap, so a hung suite is never waited on indefinitely.
- `ignore` — glob list applied to project discovery and `--all` (generated/vendored trees; the
  defaults cover `_sources/`, in-tree `node_modules/`, build output, and `.venv`). Part of the
  schema so extending it stays a configuration change.
- `nonUnitLabels` — every non-unit label in use anywhere in the workspace. Where a language's
  unit selection needs an enumerated negation (bats `--filter-tags`), it is generated from this
  list — never hardcoded in a command template.
- `languages[]` — ORDERED: during project discovery, marker precedence is array order.
- `markers` — glob patterns evaluated relative to a candidate project root (e.g. `go.mod`,
  `tests/*.bats`).
- `tools` — commands that MUST resolve from PATH before the entry runs (exit `11` otherwise).
- `probe` (optional, per selection mode) — an argv run in the project root first; a non-zero exit
  means that selection is VACUOUS for this project (exit `0`, one notice line). This keeps
  conditional tiers data-driven: python guards unit with `["test", "-d", "tests/unit"]`, JS with
  `["jq", "-e", ".scripts[\"test:unit\"]", "package.json"]` — the ENGINE never reads language
  files; only configured probes do.
- `labelPrefix` (optional) — prepended to labels in tag mechanisms (bats: `type:`).
- `labelAliases` (optional) — map from a label to its native name where they diverge (python:
  `{"contract": "contracts"}` for pd-schedule-manager's existing `tests/contracts` directory).
- `run.unit` / `run.all` — argv templates for the two RESERVED selection modes.
- `run.labels` — argv template for every other selection. Unknown labels are NOT validated; they
  pass through verbatim.
- **Placeholders** — a CLOSED set, part of the `version` contract; substitution is
  substring-level within a token. `{labels}` = the requested labels comma-joined after
  alias/prefix mapping. `{label}` = the whole argv executes ONCE PER requested label (used where
  union needs repeated invocations: bats tag union — a single `--filter-tags` value is an AND —
  and python directories). `{unitExclusion}` = the negation list derived from `nonUnitLabels`
  with `labelPrefix` (bats: `!type:integration,!type:smoke,...`). `{allLabels}` = all registered
  `nonUnitLabels` comma-joined (Go's `run.all` needs it — section 2.4). `{jobs}` = the
  parallelism width. An unrecognized placeholder is a configuration error (exit `13`).

### 2.2 CLI

```bash
pg-test-runner [--labels <csv>] --files <file>...   # prek mode: staged files in, touched projects' tests run
pg-test-runner [--labels <csv>] [<path>...]         # ad hoc: resolve each path (default: cwd) per section 2.3
pg-test-runner [--labels <csv>] --all               # every project under the repo toplevel
```

- `--labels` takes a comma-separated label list, or `all` (no filtering). Omitted, it defaults to
  `unit`. Values are NOT validated against the common vocabulary — unknown labels pass through to
  the language strategies (sections 2.1 and 2.4). Only `unit` and `all` carry reserved semantics.
- `--config <path>` overrides the configuration; normally the baked-in default is used.
- The prek hook passes `--labels unit` explicitly for self-documentation.

### 2.3 Project discovery

The marker set and its within-directory precedence come from the configuration's `languages[]`
order (defaults: `go.mod`, `pyproject.toml`, `package.json`, `tests/*.bats`). Discovered projects
are deduplicated. Paths matching the configuration's `ignore` globs are excluded from every mode.

Resolution has two modes:

- **`--files` (prek mode):** for each input file, walk UP from its containing directory to the
  nearest ancestor holding a marker — checking each directory including the git toplevel — and
  nearest marker wins. Deleted files map by path string. A file matching no project contributes
  NOTHING, deliberately: there is no downward fallback in this mode, or a commit touching only an
  unowned root-level file (README, `flake.nix`) would run every project in the repo.
- **Path arguments (ad hoc; default `cwd`):** starting AT the given directory, check it for
  markers, then walk UP — stopping when a marker is found, otherwise at the repo toplevel
  (checked as a candidate) when inside a repo, or at the filesystem root when not. If the upward
  walk finds nothing, FALL BACK to a downward scan of the subtree rooted at the ORIGINAL path,
  discovering every project beneath it. A path that yields no project in either direction exits
  `12`.
- **`--all`:** the downward scan forced from the repo toplevel — every project under the repo,
  regardless of whether the toplevel itself carries a marker. Requires being inside a repo.

The toplevel is a legitimate candidate in the upward walk: two repos keep real bats suites at the
repo root (agent-support's conformance suites, overlay's `tests/verify-provenance.bats`), and
labeling (workstream 2), not discovery, is what keeps non-unit root suites out of the commit
tier. One consequence to know: at a repo root that itself carries a marker, a bare
`pg-test-runner` resolves to the ROOT project only (the upward walk finds it immediately) — use
`--all` for the whole-repo sweep.

### 2.4 Per-language execution (Strategy pattern: one selection semantics, per-ecosystem strategies)

Run from the project root in every case. The table below is the DEFAULT configuration content —
the shipped nix attrset renders exactly these strategies (section 2.1). Unit selection uses
NEGATION (exclude the registered non-unit labels) so the unlabeled-means-unit default holds
mechanically; the bats negation list shown is the rendering of the default `nonUnitLabels`, not a
hardcoded string.

| Language | Label mechanism                                                                                                                                                                      | `--labels unit` command                                                                             | Notes                                                                                                                                                                                                                                                                                                             |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| bats     | `# bats file_tags=type:<label>` per file; `# bats test_tags=type:<label>` per-test override                                                                                          | `bats --jobs <N> --filter-tags '!type:integration,!type:smoke,!type:contract,!type:hostile' tests/` | `--jobs` always on for the unit tier (requires GNU `parallel`)                                                                                                                                                                                                                                                    |
| Go       | build tags on non-unit test files; untagged = unit (the Go idiom — unit cannot be positively tagged without breaking default builds)                                                 | `go test -race -run '^(Test\|Example)' ./...`                                                       | `-race` is part of the unit contract (it checks the parallel-safety claim). `Fuzz*` targets are excluded entirely — no seed-corpus replay in the unit tier, and `-fuzz` is never passed                                                                                                                           |
| Python   | directory is the label: `tests/unit/` = unit, `tests/<label>/` = that label (`labelAliases` where names diverge); support dirs (`factories/`, `helpers/`, `fixtures/`) are not kinds | `uv run --frozen --offline pytest tests/unit` (probe: `tests/unit` exists, else vacuous)            | Resolves from the committed `uv.lock`; may create/update the working-tree `.venv`. `--offline` keeps HK-1's no-network rule: a cold cache fails LOUD, remediation is one manual `uv sync`. The projects' declared-but-unapplied pytest markers are deliberately not used — one mechanism, zero per-test migration |
| JS/TS    | package-script split (`test:unit`)                                                                                                                                                   | `npm run test:unit` (probe: script declared in `package.json` via `jq`, else vacuous)               | `vitest run`, not watch mode; requires an existing `node_modules` (`npm run` does not install — a developer-state dependency, failure message names `npm ci`)                                                                                                                                                     |

For non-unit labels the strategies select by label, with per-language caveats stated rather than
hidden. bats `--filter-tags 'type:<label>'` and python `tests/<label>` select exactly that kind
(multi-label requests execute once per label via `{label}`). Go `-tags <label>` is a SUPERSET:
tagged files are ADDED to the default build, never replacing it, so the run is unit PLUS the
tagged kind — acceptable outside the commit path, and the reason Go's `run.all` must enumerate
`-tags {allLabels}` while bats' `all` is genuinely unfiltered. `--labels all` (or any selection
naming `contract`-like kinds) can drive credentialed or costly suites (live PagerDuty, real
Claude tokens); it is an explicit human action and MUST NOT appear in any hook or automated path.
The runner only RUNS selections; providing the fixtures/credentials such tests need is out of
scope.

### 2.5 Environment guarantees

- The runner MUST NOT invoke `nix build`, `nix develop`, `nix run`, or any flake evaluation.
- The runner sets no test environment. A unit test MUST self-resolve its source under test (the
  `SCRIPTS_DIR`-unset fallback the bash test helper already provides). This is what makes the
  same test runnable in and out of nix.
- Tools (each language entry's `tools` list — by default `bats`, `parallel`, `go`, `uv`, `npm`,
  `jq`) resolve from PATH only. A missing tool is a distinct failure naming the tool and the
  provisioning fix (add to the HM profile) — never a silent skip, never a nix fallback.
- Every project invocation is bounded by `timeoutSeconds`; expiry is reported as a failure with
  the project named, never waited out.
- **Parity requirement:** a `unit` test MUST pass both via this runner from the working tree and
  inside its project's nix `checks.*` derivation. The hermetic `checks.*` tier remains the
  authoritative thorough tier; this runner never replaces it.

### 2.6 Exit codes

Per workspace policy, exit 1 stays generic and branchable meanings are >= 2:

- `0` — all selected tests passed, including the vacuous cases (no project touched, or nothing
  matches the label selection)
- `1` — generic/unexpected error (never given a specific meaning)
- `2` — usage error
- `10` — test failures. ANY non-zero tool exit maps here, including `timeoutSeconds` expiry —
  the probes (section 2.1) keep vacuous cases from ever invoking a tool, so tool exit codes need
  no finer interpretation
- `11` — required tool missing from PATH
- `12` — an explicitly named path yields no project in either direction (no marker on the upward
  walk, none in its subtree)
- `13` — configuration missing, unreadable, or invalid (bad JSON, unsupported `version`, unknown
  placeholder)

### 2.7 Output

One summary line per project run: project path, language, pass/fail counts, duration. Silent on
vacuous prek runs.

## 3. prek integration

One hook per repo in `phillipgreenii.pre-commit.extraHooks`:

```nix
run-unit-tests = {
  enable = true;
  name = "unit tests (changed projects)";
  # entry is a small wrapper script: inside the nix sandbox (positive
  # indicator: IN_NIX_BUILD / NIX_BUILD_TOP set) print a skip notice and
  # exit 0 — the hermetic tier covers everything there. Outside the sandbox,
  # exec `pg-test-runner --labels unit --files "$@"`, optionally with a
  # repo-rendered `--config <store path>`; a missing runner FAILS loudly.
  entry = "...";
  pass_filenames = true;
  require_serial = true;
};
```

- `pass_filenames = true` — prek's staged-file list drives which projects run; the runner never
  introspects git.
- `require_serial = true` — prek may chunk a large staged-file list into multiple CONCURRENT
  invocations of the entry; serial execution keeps per-project dedup effective and prevents one
  project's suite racing itself (e.g. `uv`'s venv sync).
- The wrapper skips ONLY on a positive sandbox indicator (inside the sandboxed
  `checks.pre-commit`, where the hermetic tier already covers everything). Outside the sandbox, a
  missing `pg-test-runner` is a LOUD hook failure — an absence-keyed silent skip would let any
  PATH breakage no-op the only commit-time test gate, which this workspace treats as hook
  bypassing.
- This one hook replaces all 17 per-project `nix build` test hooks across the six repos.

### Tier model after this design

```mermaid
flowchart LR
    A["git commit"] --> B["Tier 0: scoped hygiene + unit tests of touched projects<br/>pg-test-runner --labels unit, no nix<br/>seconds"]
    C["landing / integrate-branch<br/>or explicit request"] --> D["Tier 1: thorough verification<br/>nix flake check — hermetic checks.*"]
    E["CI where present /<br/>pn workspace flake-check"] --> F["Tier 2: full sweep<br/>all systems, all checks"]
```

## 4. Non-goals

- Never runs integration/smoke/contract kinds in the commit path.
- No git introspection (the caller supplies files or project dirs).
- No per-project in-tree configuration file; the runner's configuration (section 2.1) is
  nix-owned and generated, never hand-written or carried by individual projects.
- Not a replacement for the hermetic `checks.*` tier or for the evaluation doc's Phase-3
  affected-scope landing gate (this runner is a natural seed for that gate, but the gate is a
  separate design).

## 5. Relationship to the evaluation doc

The evaluation doc's ruling line and HK-2 ("NO git hook may perform thorough verification") are
AMENDED by this design, with operator approval pending on this spec: a git hook MAY run
label-`unit` tests directly via `pg-test-runner`; a git hook MUST NOT invoke nix (build, develop,
run, or flake evaluation) or run any non-unit test kind. HK-1's scoping requirement and the rest
of the evaluation doc (Phase 2 thorough-tier completeness, Phase 3 fast-verification work) stand
unchanged. The evaluation doc MUST be updated in the same change set that lands this spec (S-1
discipline: supersede in place, cite this file).

## 6. Workstreams

1. **Runner + provisioning (repo-base, size M):** build `pg-test-runner` as a data-driven engine
   plus the nix module option that renders the default configuration JSON (section 2.1); add
   `bats`, `parallel`, and `npm` (node) to the HM profile (`go`, `uv` already present; confirm
   `jq`, which the JS probe uses, is profile-provided).
2. **Bats labeling pass (all six repos, size M):** every bats suite gets an explicit
   `# bats file_tags=type:<label>`, unit included — classification by inspection per section 1.
   Priority: labeling everything avoids confusion even though unlabeled defaults to unit. The
   artifact-coupled suites MUST be labeled non-unit in this pass, per repo, BEFORE that repo's
   workstream-5 rewiring: ziprecruiter's `Scripts/*` and `testGithubNixAuth`-style suites (built
   commands on PATH) and agent-support's repo-root conformance suites (`tests/*.bats`, which
   drive the nix-built `self-checks`). The `workflow/` suites are source-runnable (verified:
   `WF_ROOT` + mocked externals) and label `type:unit`.
3. **Go audit (size S):** every non-unit Go test file carries a REGISTERED non-unit build tag;
   platform/GOOS constraints (`//go:build darwin`, `_darwin_test.go`) are not labels and stay
   unit; confirm no `Fuzz*` target leaks into unit runs.
4. **Language-instruction updates (size S):** bash-scripting skill gains the label vocabulary,
   the unit-vs-all commands, and renames its "integration tests" wording (script-run-as-subprocess
   tests, which are unit-tier under section 1) to "script-level tests" to avoid colliding with the
   `integration` label; equivalent sections for Go and Python docs (python: record the
   directory-over-markers decision and the `labelAliases`).
5. **prek rewiring (per repo, size S each):** remove the nix-build test hooks, add the runner
   hook, update the evaluation doc per section 5. The two flagged coverage additions from the
   evaluation doc (a `pg-pr-zr-golangci` check; landing-path ownership for no-CI repos) ride
   with their respective repos' changes.
6. **Ziprecruiter `Scripts` migration (size M):** its `testScriptsWithBats` suites test nix-built
   artifacts by command name, violating the run-without-nix principle; migrate them to
   source-runnable. Until migrated they are labeled `type:integration`/`type:smoke` and stay out
   of the commit tier (hygiene hooks and the hermetic tier still cover them). agent-support's
   root conformance suites stay non-unit permanently — exercising the assembled tool is their
   point.
7. **JS unit script (size S):** declare `"test:unit": "vitest run"` in jsonl-log-parser's
   `package.json` (its bare `test` script is watch-mode `vitest`). codeburn has no `scripts`
   section at all, so the probe leaves it vacuous until tests exist.

Workstreams 1–4 and 7 are mutually independent. 5 depends on 1 (the hook needs the runner on
PATH) AND, per repo, on that repo's workstream-2 labeling — landing 5 in a repo whose
artifact-coupled suites are still unlabeled would run them at every commit (unlabeled = unit),
exactly the failure this design removes. 6 can proceed any time and simply upgrades which suites
qualify for the commit tier.

## 7. Provenance

Derived 2026-08-24 in a design session with Phillip, on top of the git-hook speed evaluation of
the same date. Facts verified against source during the session: repo-base
`flake-modules/pre-commit.nix` @ `766eef4` (base hook set, extraHooks seam),
`lib/bash-builders.nix` (`SCRIPTS_DIR` convention, `batsJobs`), ziprecruiter `nix/checks.nix`
(`testScriptsWithBats` artifact coupling) and `flake.nix` (13 `mkTestHook` hooks; tests dual-wired
as `packages.test-*` and `checks.test-*`), the bash-scripting skill (`claude-marketplace/
bash-scripting/skills/bash-scripting/SKILL.md`, "tests MUST work without nix build"), bats 1.12.0
`--filter-tags` semantics, and a direct no-nix run of
`modules/ul/determine-ul-lib-dir/tests/test-determine-ul-lib-dir.bats` (6/6 pass).

Rev 4 incorporates an independent read-only design review (2026-08-24, 13 ranked findings), which
additionally verified: jsonl-log-parser has no `test:unit` script and codeburn no `scripts` at
all (hence the probe mechanism and workstream 7); platform-constrained Go unit tests exist
(`modules/pn/internal/osx/tcc_test.go` and siblings — hence the registered-tag wording); Go
`-tags` superset semantics; untagged `Fuzz*` targets exist in two modules (the `-run` anchor is
load-bearing); agent-support's root conformance suites and ziprecruiter's `Scripts`/github-nix-auth
suites are artifact-coupled while `workflow/` suites are source-runnable; and prek's chunked
`pass_filenames` invocation behavior (hence `require_serial`).
