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
