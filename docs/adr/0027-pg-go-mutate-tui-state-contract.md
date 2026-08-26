# ADR-0027: pg-go-mutate-tui state contract — ledger schema, guard cache, semaphore, and per-package bead filing

**Date:** 2026-08-26
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

`pg-go-mutate-tui` replaces `pg-go-mutate-sweep` as this workspace's orchestrator for unattended
and multi-file mutation sweeps, moving from package-granular, unattended checkpointing to
file-granular, operator-controlled checkpointing with real N-way concurrency (design:
`docs/superpowers/specs/2026-08-25-pg-go-mutate-tui-design.md`). It exists specifically to fix
`pg-go-mutate-sweep`'s failure mode of a package too large to sweep in any bound (`pg2-mfduf`) —
file-level checkpointing plus real concurrency, not shrinking total cost.

That raises the same category of durable-state decisions ADR-0026 made for the sweep, now at file
granularity and with genuine parallelism: a ledger schema and the rule for when its package-hash
field is captured (decision 1), how the exit-code contract that ADR-0026 allocated applies once
the target is a file rather than a package (decision 2), a new guard cache needed because file
granularity would otherwise re-run the same package-level health guard once per file (decision 3),
a semaphore replacing the sweep's single exclusive lock now that more than one run can be in
flight (decision 4), and a per-package bead-filing rule where ADR-0026 decided per-project
(decision 5). Each is a compatibility surface a later reader will depend on, so per this repo's ADR
process it is recorded before being superseded again.

## Decision

### 1. Ledger schema and the dispatch-time hash-stamping rule

`pg-go-mutate-tui`'s ledger is an append-only JSON-lines log at
`${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-tui/ledger.jsonl`. Each record is:

```json
{
  "file_path": "…",
  "package_hash": "…",
  "status": "done",
  "report_path": "…",
  "root_git_hash": "…",
  "timestamp": "…"
}
```

It is a work-avoidance cache, not a source of truth that must survive a crash byte for byte:
losing an unflushed record only costs one redundant, idempotent re-analysis on the next run.
Replay keeps the last record per `file_path` and tolerates a truncated trailing line (an
in-progress worker's hard kill mid-append is an expected input shape, not corruption) — it is
never treated as replay failing outright.

**Dispatch-time hash-stamping**: `package_hash` is computed exactly once, when the queue refills
and selects a file for work (a real content digest over the package's `.go` files, not the
package path), and is carried unchanged from queue entry → worker → ledger record. It is never
recomputed at pop time or at run time. This matters because a live, operator-driven session can
run for hours: a package can be edited after a file is queued but before a worker actually pops
and analyses it. Stamping the hash at dispatch time means the ledger records "this file was
analysed against the package as it existed when the file was selected for work" — an unambiguous,
reproducible claim — rather than silently validating against whichever content happened to be on
disk when the worker got around to it.

### 2. Exit-code contract reuse, now evaluated per file

ADR-0026's exit-code allocation (`10`–`14`, additive to `0`/`1`/`2`) is unchanged and is NOT
re-allocated here. What changes is only the unit it is evaluated against: `pg-go-mutate --json
<file>.go` (Task 1's file-target extension) evaluates the contract against one file's containing
package, not a whole directory tree. `pg-go-mutate-tui`'s worker classifies each run's exit code
into a ledger status (`done`/`no-tests`/`not-enumerable`/`unhealthy`/`vanished`, default `failed`)
— except `13` (environment precondition failed), which is never recorded per file: it fails
identically for every remaining file, so the worker pool aborts the whole run on it
(`worker.ErrFatal`) rather than recording one identical failure per file, the same rationale
ADR-0026 decision 3 gave for the sweep's own abort-on-`13` behavior.

### 3. Guard-cache mechanism

File granularity would otherwise re-run the same package-level health guard
(`pgm_tests_healthy`) once per file in an unchanged package. The guard cache under
`${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate/guard-cache/` is keyed on a filesystem-safe
slug of the package directory plus that package's real content hash: a cached `PASS` skips
re-running the guard for a second file in the same unchanged package, and a cached `FAIL` makes
`pg-go-mutate` exit `12` without re-running the guard at all. Editing any file in the package
changes the hash, which is a cache miss and re-runs the guard on the next invocation — the cache
can never serve a stale verdict for changed content.

### 4. Semaphore replacing the sweep's exclusive lock

The sweep's single exclusive lock is replaced by an N-slot semaphore
(`${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate/sem/`, capacity read from the static
nix-rendered config's `concurrency`, default `1`). A slot whose stamped PID is no longer alive is
reclaimed, the same stale-holder recovery the old lock had, generalized from one holder to
`capacity` holders. Every semaphore-dispatched `gomu run` is forced to `--workers 1` regardless of
what the caller passed to `pg-go-mutate` itself, and `GOMAXPROCS` is capped to `nproc /
concurrency` (floored at `1`) before each invocation — so `N` concurrent `pg-go-mutate-tui` workers
cannot oversubscribe the machine the way `N` independently-invoked `--workers K` runs could.

### 5. Per-package triage bead, never an epic

On finishing every file in a package, `pg-go-mutate-tui` files (or amends) exactly one bead: `P3`,
labeled `go-test-gaps` plus the caller-supplied canonical repo label, title `go-test-gaps triage:
<pkgPath>`, body carrying the file-status tally only — never a survivor or kill count. It MUST NOT
file an epic, for the identical reason ADR-0026 decision 4 gave the sweep: an open epic never
leaves `bd ready`. The granularity is finer than ADR-0026's per-**project** rule: `pg-go-mutate-tui`
completes packages incrementally as it works through a project's queue, so a per-package bead
surfaces triage-worthy findings as they finish rather than batching them behind an entire project.

### Rejected alternatives

- **Recomputing the package hash at pop time instead of dispatch time.** Rejected: a live session
  can span hours, and a queued file's package can be edited between being queued and being popped.
  Recomputing at pop time would silently validate the run against content that no longer matches
  what was queued, corrupting the ledger's claim of "analysed at this hash."
- **Keeping the sweep's single exclusive lock.** Rejected: it forced every mutation run — bare or
  under the sweep — onto one global sequential queue even on a multi-core machine, which is
  exactly the "too large to sweep in any bound" failure this epic exists to fix.
- **Per-project bead filing, matching ADR-0026 exactly.** Rejected: `pg-go-mutate-tui`'s natural
  unit of progress is the package, and by the time an entire project finishes, incremental
  per-package findings would have been withheld from the operator for no reason.

## Consequences

### Positive

- The queue/worker/ledger triangle cannot record a false "this file's current package hash was
  analysed" — the hash is pinned at the exact moment the file was selected for work.
- A guard failure common to many files in one package costs one guard run per package edit, not
  one run per file.
- `N`-way concurrency comes with an enforced ceiling (`--workers 1`, capped `GOMAXPROCS`) instead
  of trusting every caller to self-limit.
- The tool family's score prohibition is enforced at two independent layers: the ledger schema
  (status only) and the bead body (tally only) — neither can regress into a survivor/kill count
  without a review catching the shape change.

### Negative / Neutral

- ADR-0026's exit-code contract is now evaluated against smaller units (files, not whole
  packages); a caller that assumed "`12` means the whole package is unhealthy" must re-read it as
  "this run's guard failed," though the code's meaning is otherwise unchanged.
- `pg-go-mutate-sweep` is retired outright, not kept as a fallback: there is no more
  package-granular, unattended mode. The file-granular, operator-attended TUI is the only
  orchestrator going forward.
- The semaphore, guard-cache, and ledger state each live under their own path; a future reader
  auditing `pg-go-mutate`'s on-disk footprint must know to check three separate roots, not one.

## Related Decisions

See also: `docs/adr/0026-mutation-sweep-state-contract.md` (superseded by this ADR).
