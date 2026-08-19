# pg-go-mutate-sweep

> Resumable unattended mutation sweep over every Go package in the workspace.
> Runs `pg-go-mutate` one unit at a time and files one triage bead per project.
> More information: <https://github.com/phillipgreenii/nix-repo-base>.

- Sweep the whole workspace (re-run the same command to resume where it stopped):

`pg-go-mutate-sweep`

- Print the plan and the resume position without running anything:

`pg-go-mutate-sweep --dry-run`

- Restrict the run list to one project (repeatable):

`pg-go-mutate-sweep --only {{phillipgreenii-nix-agent-support/packages/pb}}`

- Re-attempt the units whose failure a re-run might change:

`pg-go-mutate-sweep --retry transient`

- Re-attempt units by explicit status (comma-separated):

`pg-go-mutate-sweep --retry {{timeout,unhealthy}}`

- Redo one unit with its withheld tags applied and a raised timeout:

`pg-go-mutate-sweep --redo {{project/key#internal/gate}} --auto-tags {{contract}} --mutant-timeout {{180}}`

- Analyse and record without filing any bead:

`pg-go-mutate-sweep --no-beads`

- Break a lock whose holder is gone or wedged:

`pg-go-mutate-sweep --force-unlock`

- Show usage:

`pg-go-mutate-sweep --help`

## Reading the state

State is durable and lives under `${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-sweep/`
(ADR-0026), deliberately outside any session-scoped directory a teardown could reclaim:

| Path                                  | What it holds                                                 |
| ------------------------------------- | ------------------------------------------------------------- |
| `ledger.jsonl`                        | Append-only, one record per attempt. Replayed to resume.      |
| `runs/<project-slug>/<pkg-slug>.json` | That unit's worklist, overwritten in place on each attempt.   |
| `lock/`                               | Lock directory, stamped with the holder's PID and start time. |

A unit is one `(project, package)` pair, keyed `<project>#<package>`. The ledger records unit
**status only** — `done`, `no-tests`, `failed`, `timeout`, `unhealthy`, `not-enumerable`,
`vanished`, `inconclusive` — and **never a survivor count, percentage, or score**. Resume state is
derived by replaying the ledger and keeping the last record per key, so a killed sweep needs no
repair.

The actionable worklist is `.survivors` in each unit's JSON: every entry names a file, a line and
the mutation operator. Read `tags_withheld` on the unit's ledger record FIRST — a non-empty value
means that unit's tests were partially gated behind build tags that were not applied, so its
survivors are unanalysed and must not be filed as gaps until it is re-run with `--auto-tags`
widened deliberately. A partially-gated unit records `done` and is indistinguishable from a genuine
gap without that check.

By default no recorded status is re-attempted, so every run makes forward progress and can never
loop on a broken unit; `--retry` is the deliberate opt-out.

## Exit status

A unit's recorded failure never changes the exit status — that is the point of an unattended sweep.

| Code | Meaning                                                                                           |
| ---- | ------------------------------------------------------------------------------------------------- |
| `0`  | The sweep completed, or nothing was left to run.                                                  |
| `2`  | Usage error, or a plan-time defect such as a slug collision.                                      |
| `3`  | Another sweep holds the lock.                                                                     |
| `4`  | Fatal abort: a missing prerequisite, or a cause that would fail every remaining unit identically. |
