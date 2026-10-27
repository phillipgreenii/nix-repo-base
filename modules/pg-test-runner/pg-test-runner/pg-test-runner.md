# pg-test-runner

> Label-driven, nix-free-at-runtime direct test runner.
> Runs a project's unit tests straight from source — no `nix build`, no `nix develop`.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Run the unit tier for the current directory (default when no labels are given):

`pg-test-runner`

- Run the unit tier for a specific project path:

`pg-test-runner {{modules/ul/determine-ul-lib-dir}}`

- prek mode: resolve staged files to their owning projects and run those:

`pg-test-runner --files {{file1 file2}}`

- Every project under the repo, regardless of location:

`pg-test-runner --all`

- Run a specific non-unit label (never in a commit hook):

`pg-test-runner --labels {{integration}} {{path}}`

- Run every label, unfiltered (an explicit human action; costs live credentials/tokens on
  `contract`-kind suites):

`pg-test-runner --labels all --all`

- Override the configuration registry:

`pg-test-runner --config {{/path/to/config.json}} {{path}}`

- Show usage:

`pg-test-runner --help`

## Timeouts

Every project invocation is bounded by the config's `timeoutSeconds` (default `300`). A single
slow project can raise ITS OWN cap through the optional `projectTimeouts` map in the same config:
keys are project path suffixes (no leading or trailing `/`) that match a project directory when
equal to it or to a trailing run of its path components, values are positive integer seconds, and
the longest matching key wins. Projects not named keep the global cap. A malformed
`projectTimeouts` is exit `13`.

```json
{
  "timeoutSeconds": 300,
  "projectTimeouts": { "packages/claude-extended-tool-approver": 900 }
}
```

## Concurrency and niceness

Every project invocation first takes one of `maxConcurrentRuns` host-wide slots (default `2` in the shipped config;
`0` = unlimited) shared by ALL pg-test-runner processes on the machine, so concurrent commit-time
and pre-land hook runs from many sessions queue instead of piling `go test -race` onto the same
cores. Queue time never counts against the `timeoutSeconds` cap. If no slot frees up within
`slotWaitSeconds` (default `600`) the run proceeds anyway with a warning (fail open). Each run
executes under `nice -n niceLevel` (default `10`; `0` = off). Slots are symlinks under
`/tmp/pg-test-runner-slots.<uid>` recording `<pid>:<start time>`; a slot whose owner is dead is
reclaimed automatically. The bound is a soft cap (a reclaim race can let one extra run through).

Per-run environment overrides win over the config: `PG_TEST_RUNNER_MAX_CONCURRENT_RUNS`,
`PG_TEST_RUNNER_SLOT_WAIT_SECONDS`, `PG_TEST_RUNNER_NICE_LEVEL` (non-negative integers; anything
else is exit `2`) and `PG_TEST_RUNNER_LOCK_ROOT`. A malformed config value is exit `13`.

```json
{
  "maxConcurrentRuns": 2,
  "slotWaitSeconds": 600,
  "niceLevel": 10
}
```

## Running several projects at once

`projectConcurrency` (default `1`) is how many projects one pg-test-runner run starts at the same
time. With `1` the projects run one after the other. With `N > 1`, up to `N` run concurrently, each
with its output buffered and replayed in discovery order after all of them finished (so output
never interleaves, and nothing prints until the whole run ends). The exit status is unchanged:
`10` if any project failed, else `11` if a tool was missing, else `0`. The host-wide
`maxConcurrentRuns` cap still applies, so a value above it only queues. Raise it only for a repo
whose projects share no state (a port, a fixed path, a database). The environment variable
`PG_TEST_RUNNER_PROJECT_CONCURRENCY` overrides the config for one run (a non-positive or
non-integer value is exit `2`); a malformed config value is exit `13`.

```json
{ "projectConcurrency": 2 }
```

## Which changed files run a project's suite (`--files` mode)

In `--files` mode (the `run-unit-tests` commit hook) a project is not run just because some changed
file lives under it. A language MAY carry an `inputs` block in the config; for such a language the
project's suite runs only when at least one changed file under it is a test input, and otherwise the
project is skipped with one line per skipped project:

```text
[SKIP] /repo/packages/pg-connector (go) none of the 2 changed file(s) under it is a test input (source, module file, testdata, embedded or declared extra path); suite not run
```

A language WITHOUT `inputs` (python, js, bats in the shipped config) runs on any changed file, as
before. Path arguments and `--all` never skip.

For the shipped `go` language a changed file is a test input when ANY of these holds:

- its basename matches `inputs.names`: `*.go` (so `_test.go` too), `go.mod`, `go.sum`, `go.work`,
  `go.work.sum`, and cgo/assembly sources (`*.c`, `*.h`, `*.s`, `*.cc`, `*.cpp`, ...);
- a directory component of its path is in `inputs.dirs` (`testdata`, `vendor`), even though
  `testdata/` is an `ignore` entry for project discovery;
- it matches a `projectExtraPaths` entry for the project (below);
- `inputs.goEmbed` is true and a `//go:embed` directive in a Go file of the project names it. The
  runner scans the project's `*.go` files (working tree) for directives, reads quoted (`"..."`,
  backquoted) and bare patterns and the `all:` prefix, and resolves each pattern relative to the
  directory of the file that holds it; a pattern naming a directory covers everything below it. A
  directive it cannot read with certainty (unterminated quote, no pattern, a backslash escape, an
  absolute or `..` pattern, or a grep failure) makes EVERY changed file count for that project.

When in doubt the suite runs. A changed file that does not match any rule, but that a test reads by
path (a golden file outside `testdata/`, a YAML fixture), is NOT detected: declare it in
`projectExtraPaths`, a map from a project path suffix (same matching as `projectTimeouts`, every
matching key contributes) to non-empty glob patterns relative to the project root. `*` also matches
`/`, and a pattern naming a directory covers everything below it:

```json
{
  "projectExtraPaths": {
    "packages/pg-connector": ["grafana/*.yaml", "contracts"]
  }
}
```

A malformed `projectExtraPaths` or `languages[].inputs` is exit `13`. The shipped default registry
is `modules/pg-test-runner/config.nix` (its `projectExtraPaths` is empty; add entries there or in a
machine-customised `phillipgreenii.pg-test-runner.config`).

## Exit codes

| Code | Meaning                                                                |
| ---- | ---------------------------------------------------------------------- |
| `0`  | All selected tests passed, including vacuous cases (nothing to run).   |
| `1`  | Generic/unexpected error.                                              |
| `2`  | Usage error: bad flags, mode conflict, or a nonexistent path argument. |
| `10` | One or more test invocations failed, including a `timeoutSeconds` cap. |
| `11` | A required language tool was missing from `PATH`.                      |
| `12` | A path argument resolved to no project, upward or downward.            |
| `13` | Configuration missing, unreadable, or invalid.                         |

`--files`, path arguments, and `--all` are mutually exclusive input modes; combining them is a
usage error. The runner never invokes `nix`, never inspects git state, and only reads whichever
languages its configuration registers — adding a language, changing a command, or registering a
label is a configuration change, never a code change.
