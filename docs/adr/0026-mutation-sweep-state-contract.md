# ADR-0026: Mutation-sweep durable state — layout, ledger schema, and exit-code allocation

**Date:** 2026-08-17
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

`pg-go-mutate-sweep` is a new resumable, unattended runner that invokes the existing
`pg-go-mutate` mutation-testing diagnostic once per Go package across this workspace's six repos
(design: `docs/superpowers/specs/2026-08-17-pg-go-mutate-sweep-design.md`, companion to
`2026-08-14-pg-go-mutate-design.md`). A single invocation costs roughly `mutants x that package's
test-suite runtime`; measured on this workspace on 2026-08-17, the smallest of the six repos alone
took 38 minutes, and the two largest carry over a thousand test functions apiece across three
dozen package directories. Sweeping the whole workspace — roughly 216 units — cannot happen in one
sitting.

Three failures were observed running that by hand:

- A full sweep's results were written to a session-scoped scratchpad and destroyed when that
  directory was reclaimed two days later; the analysis had to be redone from scratch.
- A whole-module invocation is the wrong resumable unit: nothing partial survives an interruption.
- `pg-go-mutate` reports unsatisfied custom build tags but does not apply them, so mutants covered
  only by a tag-gated test silently appear as survivors, and an operator across sixteen projects
  cannot be expected to remember which packages those are.

Recovering from any of that means the sweep's progress must be durable, replayable, and outlive
any one session — which raises where that state lives and how it is keyed (decisions 1–2 below).
Separately, `pg-go-mutate` today collapses every guard failure into a bare `exit 1` (reserving `2`
for usage errors), which is not enough for the sweep to tell "this package genuinely has no tests"
apart from "the sweep built a bad invocation" apart from "the environment is broken for every
unit" — the last of which must abort the whole sweep rather than record 216 identical failures.
Distinguishing those means allocating new exit codes on a shipped, public command (decision 3).
And because the sweep files findings as beads rather than reports, how it groups those findings per
project is a decision it should not revisit for each of the 16 projects it covers (decision 4).

Each of these is a compatibility surface a later reader — or another consumer of `pg-go-mutate`'s
exit status — will depend on, so per this repo's ADR process
(`0000-use-architecture-decision-records.md`), the decision is recorded before the implementation
lands.

## Decision

### 1. State root and layout

The sweep's durable state lives outside any session-scoped directory, at
`${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-sweep/`, matching the workspace's existing XDG
convention:

```text
ledger.jsonl                          append-only, one record per attempt
runs/<project-slug>/<pkg-slug>.json   the unit's worklist, overwritten in place
lock/                                 lock directory, stamped with PID and start time
```

Rationale: a scratchpad reclaim destroyed a full sweep's output on 2026-08-15; the state root MUST
be somewhere no session teardown can touch.

### 2. Ledger schema

`ledger.jsonl` is an **append-only** log. Every record carries a discriminating `kind`, of which
there are exactly two: `unit` (one attempt at one package) and `bead` (one triage-bead filing or
amendment). All resumable state — which units are done, which projects have a filed bead — is
derived by REPLAYING the ledger, keeping the LAST record per key; there is no other state to
reconcile after a crash.

A unit record carries STATUS only (`done`, `no-tests`, `failed`, …) and never a mutant or survivor
count. This follows the tool family's score prohibition (design spec section 3, N1): the ledger
MUST NOT become a time series of counts, only of unit outcomes.

The **unit key** is `<project-key>#<pkg-path>` — e.g.
`phillipgreenii-nix-agent-support/packages/pb#internal/gate` — where the **project key is the
workspace-root-relative path**, not the project's basename (a basename is not unique across six
repos). `#` is the separator because it occurs in neither component, so the key parses back
unambiguously even though both halves contain `/`.

### 3. Exit-code allocation for `pg-go-mutate`

`pg-go-mutate` gains five new exit codes, additive to its existing `0`/`1`/`2`:

| Code | Meaning                                                                          |
| ---- | -------------------------------------------------------------------------------- |
| `10` | Target has no test files.                                                        |
| `11` | Target not enumerable.                                                           |
| `12` | Target unhealthy: does not vet, or tests already fail on unmutated source.       |
| `13` | Environment precondition failed: `go` or the pinned engine absent or mismatched. |
| `14` | Target path is absent or not a directory.                                        |

This is the only one of the four decisions that amends a SHIPPED, public command rather than
introducing new state, which is why it is the most compatibility-relevant of the four. It is safe
because the change is strictly additive: `10`–`14` are unused today, and every existing consumer
(the tool's bats cases, `pg-go-mutate.md`, the `go-test-gaps` skill, the companion design spec)
asserts only the `0`/non-zero dichotomy, never a specific non-zero value. `13` exists because it
fails IDENTICALLY for every unit in a sweep and must abort the whole run rather than record 216
failures for the same cause; `14` exists so a package directory that vanishes mid-sweep — a live
workspace can have branches switched under it while a multi-hour sweep runs — is distinguishable
from the sweep having built a malformed invocation.

### 4. One triage bead per project, never an epic

On finishing a project, the sweep files (or amends) exactly ONE bead carrying that project's
findings and the triage protocol needed to act on them — never an epic. Rationale: an open epic
sits in `bd ready` permanently, so an epic-shaped bead for each of 16 long-lived projects would
never leave the ready queue.

### Rejected alternatives

- **Classifying by string-matching `pg-go-mutate`'s stderr.** Rejected as unversioned coupling to
  interpolated prose — a wording change in the tool's error messages would silently break the
  sweep's classification, with no version boundary to catch it.
- **Storing state in the repo.** Rejected: it would commit machine-local sweep progress into
  version control, and the sweep's artifacts (the ledger, the per-unit reports) are regenerable by
  re-running the sweep, not something worth versioning.

## Consequences

### Positive

- Interruption and correction are both survivable: any resumed sweep replays the ledger and makes
  forward progress, and a targeted `--redo`/`--retry` needs no hand-editing of state.
- The sweep's classification of a unit's outcome no longer depends on parsing another script's
  prose, so it cannot silently break when that prose changes.
- `pg-go-mutate`'s existing consumers are unaffected: nothing asserts a specific non-zero code
  today, so the new codes are purely additive.
- 16 triage beads, not 16 epics, stay finite and closeable, and never permanently occupy
  `bd ready`.

### Negative / Neutral

- `pg-go-mutate`'s exit contract grows from three codes to eight; a future consumer that wants to
  distinguish a specific failure must now pick one of `1`/`10`/`11`/`12`/`13`/`14` rather than a
  bare "non-zero", but enabling that distinction is the point of adding them.
- The ledger's append-only design grows without bound across repeated sweeps; nothing in this ADR
  prunes it. A user-level backup of the state root is outside the tool's own control and is not a
  count series the tool itself creates (N1's scope note).
- The XDG state root is per-machine, not shared across agents or CI, matching every other piece of
  durable state this workspace already keeps there.
