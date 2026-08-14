# Applied-state records the terminal's locked revs at apply time

**Status**: Accepted
**Date**: 2026-08-14
**Deciders**: Phillip Green II (with Claude)
**Tracking**: pg2-ft60a

## Context

ADR [0012](0012-pn-applied-state-store-and-info-api.md) defined a per-repo applied-state record
whose `applied_ref` is the repo's local `git rev-parse HEAD` at apply time, and published it through
`pn workspace info --json`. Its consumer — the `pn:applied` gate in
`phillipgreenii-nix-agent-support`'s `pb` (that repo's ADR 0018) — holds a follow-up bead (canonically
"verify the code works") out of `bd ready` until the change it depends on has been applied, and
resolves the gate when the gated change's `git patch-id` appears in
`applied_baseline..applied_ref`.

That single fact is not enough to answer the question the gate asks.

`applied_ref` proves an apply RAN over a checkout that contained the commit. It does not prove the
applied SYSTEM contains it. For a repo the terminal consumes as a **remote flake input** — a
`github:` pin — the code reaches the build through the terminal's `flake.lock`. A commit landed on
local `main` but never pushed and relocked is therefore absent from the built system while
`applied_ref` reports it as applied.

Measured on this workspace: `strings $(readlink -f $(command -v pn)) | grep -c ruff-pin` is `0`
while that work IS on `phillipg-nix-repo-base`'s local `main`, and the installed `pn` store path has
moved since — so applies have run in between and the change is still absent.

The consequence is not cosmetic. The gate exists so that a peer draining `bd ready` does not claim a
verification bead and "verify" against un-applied code. A false resolve makes the verifier most
likely conclude "the feature is broken" rather than "it was never deployed" — inviting a revert of
correct work. It happened: gate `pg2-fci69` released `pg2-c40r4` against code no build had seen.

An earlier attempt tried to fix this by **redefining** `applied_ref` to mean the built rev (the
terminal's locked rev for a flake-input repo, local HEAD for the terminal). That is preserved on
branch `parked/pg2-ft60a-applied-ref-experiment` and was NOT adopted. Redefining the field
overloads one slot with two different claims, and it breaks `needsRebuild`, which compares local
HEAD against `applied_ref` to decide whether anything changed since the last apply: pitted against a
locked rev that normally differs from HEAD, the skip gate never fires and every apply rebuilds. That
experiment had to move the rebuild key to a new field to compensate.

It also matters WHEN the lock is read. Asking "is the terminal's lock NOW past the gated commit?"
reintroduces the same false resolve in a narrower window: an apply at T1 followed by a relock at
T2 > T1 would satisfy the test while the running system was built from the pre-relock rev.

## Decision

The applied-state record gains a **second, independent fact** rather than a redefinition. Schema
version **2**:

```json
{
  "schema": 2,
  "applied_ref": "<git rev>",
  "locked_revs": { "<repo key>": "<git rev>" },
  "dirty": false,
  "applied_at": "<RFC 3339 timestamp>"
}
```

### 1. `applied_ref` is UNCHANGED

It remains the repo's local HEAD at apply time, and it remains the `needsRebuild` key. Every
existing consumer keeps working. Its meaning is narrowed only in documentation: it is evidence that
an apply RAN, not that the built system contains that commit.

### 2. `locked_revs` records the terminal's lock AT THAT APPLY

`markApplied` resolves, for each workspace repo the terminal consumes as a flake input, the rev the
terminal's `flake.lock` pins for it, and writes that map into every repo's record. The map describes
the apply, so each record is self-contained evidence about the build that produced it.

It is recorded WITH the apply and MUST NOT be re-read at query time. That is what closes the T1/T2
ordering hole above.

The repo → rev mapping composes two mechanisms that already exist:

- `Lock.Edges` maps `(consumer, alias) → target repo`, and those edges were derived by matching
  `canonicalURL(flake input URL)` against `canonicalURL(the repo's configured remote)`. The
  canonical form is `host/owner/repo`, so the OWNER is inherently part of the match and two
  same-named repos under different owners cannot be crossed — this workspace has exactly that shape.
  It is also the SAME edge set `apply` derives its `--override-input` flags from, so the override set
  and the recorded lock evidence always describe the same list of repos.
- `alias → rev` goes through `readAliasRevs`, which resolves the alias exactly as nix does:
  `root.inputs[alias]` names a node key, and that node's `locked.rev` is the rev. `flake.lock` node
  KEYS are never matched against: they neither equal the workspace repo key
  (`phillipgreenii-nix-base` is the node for repo `phillipg-nix-repo-base`) nor stay stable (nix
  appends `_2`/`_3` to disambiguate).

### 3. The KEY SET is part of the claim — three states, read the key not the value

| state                           | meaning                                                                                        | consumer                      |
| ------------------------------- | ---------------------------------------------------------------------------------------------- | ----------------------------- |
| key ABSENT                      | the terminal does not consume this repo as a flake input (the terminal itself, or a non-input) | no lock claim applies — SKIP  |
| key present, value NON-EMPTY    | the rev the terminal's `flake.lock` pinned                                                     | test the gated commit ag't it |
| key present, value EMPTY (`""`) | it IS an input, but the rev could not be established                                           | FAIL CLOSED                   |

The empty-value state MUST be written rather than dropped. Dropping it would downgrade "the apply
cannot say what it built this input from" into the indistinguishable "not an input" SKIP, which is
fail-open — the very defect being fixed. `markApplied` also announces it on stdout, because a
silently unprovable apply is this bead's failure mode.

The terminal repo is sound **by construction, with no special case**: the apply builds it from its
local directory (`{terminal_nix_dir}`), so it has no entry, so the lock condition is skipped and its
gates keep resolving on `applied_ref` alone.

### 4. `schema` distinguishes "no information" from "no entry"

A record written before this change has no `schema` key (reads back as `0`) and no `locked_revs`.
`readAppliedState` performs **no migration** and deliberately does NOT stamp the version forward:
every field an older record carries still means exactly what it meant, and `0` is the honest
statement "this record carries no lock information", which is what a consumer keys its
backwards-compatibility branch on. Records gain `locked_revs` on the next successful apply.

Distinguishing by VERSION rather than by "the map is empty" is required because "old record, no
information" and "current record, this repo genuinely is not an input" are identical if you probe
the map alone, and they are not the same claim. The consumer skips the lock condition for schema `<`
2; the alternative — treating a missing map as unprovable — makes every gate unresolvable until a new
`pn` is built, pushed, relocked and applied, and this very fix ships through that path.

### 5. `pn workspace info --json` publishes the projection

`repos[]` gains three fields, per ADR 0012's "new optional fields MAY be added":

- `applied_state_schema` — the record's schema version (`0` when absent or no record).
- `terminal_input` — whether that apply consumed the repo as a flake input of the terminal
  (`locked_revs` key presence). Meaningful only when `applied_state_schema >= 2`.
- `locked_rev` — the rev pinned for THIS repo, `""` when unestablished.

Nothing is renamed or removed, so an existing consumer is unaffected. The human-readable `info`
output prints `locked <rev>` beside the applied column for a flake-input repo, so a lone rev can
never read as "this checkout is what is applied".

### 6. Gate CREATION is deliberately untouched

Gating a commit that is not yet pushed and relocked is normal and correct — the gate exists to hold
the follow-up until it ships. There is no creation-time refusal, warning, or opt-out flag. The whole
remedy is on the resolution side.

## Consequences

### Positive

- A `pn:applied` gate on an unpushed commit in a `github:`-pinned repo can no longer resolve, so a
  verification bead is not released against code no build has seen.
- A relock after an apply cannot retroactively resolve that apply's gate.
- `needsRebuild` and every ADR-0012 consumer are untouched; the rebuild-skip gate keeps its exact
  behaviour, which the parked redefinition could not manage without a compensating field.
- The unprovable case is audible at apply time and reported by the consumer, rather than silently
  resolving.
- The repo→rev mapping reuses the lock edge set the apply itself uses, so the evidence cannot
  describe a different set of repos than the build did.

### Negative

- A gate on a flake-input repo now requires push + relock + apply before it resolves. Committing
  locally and applying is no longer sufficient. This is the accepted fail-closed trade-off: the
  ancestor case (commit pushed, terminal not yet relocked) also stays blocked.
- The apply's whole `locked_revs` map is duplicated into every repo's record. It is a handful of
  short strings per repo, and it buys a self-contained record.
- A workspace lock with no edges yields no entries, so the lock condition is skipped for every repo.
  That is correct for a terminal with no workspace flake inputs — and it is the same edge set the
  apply passes as overrides, so such an apply overrode nothing — but it does mean an unlocked
  workspace gets ADR-0012 behaviour rather than a hard failure.

### Neutral

- `applied_ref`'s narrowed documentation does not change any byte on disk, so no consumer needs to
  act on it.
- ADR 0012 is **amended**, not superseded: its store path, its canonical-path keying rule, and its
  schema-stability contract all stand.
