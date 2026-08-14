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

## Amendment: the Context measurement was invalid, and `apply` does not build workspace repos from the lock (bd pg2-xg9wp)

The **decision stands** — `locked_revs` is still correct, self-contained, fail-closed evidence — but
two Context claims do not survive re-measurement, so `locked_revs` MUST NOT be read as "the rev this
apply built that repo from".

1. **The `strings … | grep -c ruff-pin` reading of `0` is an artifact of the probe.** `bin/pn` is a
   1158-byte `makeWrapper` bash script; the Go binary is the sibling dotfile `bin/.pn-wrapped`, which
   `ls` hides and `readlink -f` does not reach. The same probe returns `0` for `branch-synced` and
   for bare `ruff`; run against `.pn-wrapped` it returns `3`. The ruff-pin work was in the terminal's
   pinned `nix-base` rev (`d8b5972`) all along.

2. **`pn workspace apply` overrides EVERY non-terminal workspace repo, so a `github:`-pinned repo is
   built from the local clone at EVAL-TIME HEAD, not from the lock.** `Apply` computes
   `overrideInputArgsFor` unconditionally (`apply.go`) and appends one
   `--override-input <alias> git+file://<clone>` pair per lock edge (`helpers.go`). Evidence from
   the active generation: system `795`'s `claude-extended-tool-approver` source is byte-identical to
   `phillipgreenii-nix-agent-support` at `fc3bf2e9` — seven commits AHEAD of the terminal's pinned
   `2b18e16` — and its digest moved `bf2dc569 → 34d5a4a5 → 5550dc75` across generations
   `793 → 794 → 795` while the lock's rev never moved. Generation `794` is decisive on its own: its
   CETA source is agent-support at `9e3bb00f` (14:25), which is NOT an ancestor of `2b18e16`, and the
   terminal's `flake.lock` was last touched at 13:13. A lock-built system could not contain it.

3. **The staleness the Context reported is a TOCTOU in `markApplied`, not a lock problem.** It reads
   `git rev-parse HEAD` AFTER the rebuild, so a commit landing during the apply window is recorded as
   `applied_ref` although the activated system was evaluated before it. Measured for generation `795`
   via `nix path-info --json … .registrationTime` and `git reflog show main --date=iso`: the system
   `.drv` was written `15:36:41`, its output registered and activated `15:38:34`, and the
   applied-state files written `15:39:51` — while repo-base `main` advanced to `458b5e4` at `15:36:28`
   and agent-support `main` to `974d0276` at `15:37:00`. Both were recorded as `applied_ref`; neither
   is in generation `795`.

Consequence for `phillipgreenii-nix-agent-support` ADR 0046: its condition 2 is sound as a
fail-closed test but stricter than the build requires, because an override'd repo's built code is its
local eval-time HEAD, which normally LEADS `locked_revs[repo]`. A `blocked` verdict therefore means
"not provable from the lock", NOT "not in the running system". Closing the TOCTOU requires the apply
to capture each override'd checkout's HEAD BEFORE the build; that is not done today.

## Note: `locked_revs` stays as-is; schema 3 records WHAT THE APPLY OVERRODE alongside it (bd pg2-14yqh)

**Date**: 2026-08-14
**Tracking**: pg2-14yqh
**Provenance**: operator ruling by `phillipg@ziprecruiter.com`, 2026-08-14, recorded on bead
`pg2-14yqh`. The consuming rule lives in `phillipgreenii-nix-agent-support` ADR 0046's amendment
"condition 2 is CONDITIONAL on whether the apply OVERRODE the repo" — read it for why.

**Nothing in the Decision above is withdrawn.** `locked_revs` is still recorded for every terminal
flake input, still recorded WITH the apply rather than re-read at query time, still carries the same
three states read by KEY presence, and is still the fail-closed evidence a lock-built input's gate is
tested against. The operator ruling explicitly left it in place.

What the amendment above established is that it is not, by itself, the whole story: an apply
OVERRIDES every terminal lock edge whose clone exists, and such a repo is built from that clone at
eval-time HEAD, not from `locked_revs[repo]`. The consumer therefore needs a second fact — WHICH
inputs this apply overrode — and the applied-state did not carry it. Schema **3** adds it:

```json
{
  "schema": 3,
  "applied_ref": "<git rev>",
  "locked_revs": { "<repo key>": "<git rev>" },
  "overridden_inputs": { "<repo key>": "git+file://<local dir>" },
  "dirty": false,
  "applied_at": "<RFC 3339 timestamp>"
}
```

- **The bump is purely additive**, exactly as schema 2 was. No field changes meaning, `readAppliedState`
  still performs no migration, and `needsRebuild` still keys on `applied_ref`.
- **Read the KEY SET, not the values.** A key present means the build read the repo from that local
  directory; absent means nix resolved it from the terminal's `flake.lock`. Unlike `locked_revs`
  there is no third state — the value is never empty for a present key. It is diagnostic: under a
  coordinated-worktree (override-path) apply it names the set member the build actually read, which is
  not `<root>/<name>`.
- **The key set is a SUBSET of `locked_revs`'.** Both come from the terminal's lock edges;
  an override additionally requires the clone to exist on disk. That gap is the only state in which a
  workspace repo is genuinely lock-built, and it is what keeps the consumer's lock condition
  meaningful rather than dead.
- **One resolution, two projections.** `Apply` resolves the override set ONCE and derives both the
  emitted `--override-input` flags and the recorded map from it, then hands the map to `markApplied`.
  It is a parameter rather than recomputed after the build because the set depends on which clones
  exist: a second resolution would re-stat the filesystem and could record an override nix was never
  given (fail-OPEN for the consumer) or miss one it was.
- **`pn workspace info --json` publishes `overridden`** per repo, per ADR 0012's "new optional fields
  MAY be added". Meaningful only when `applied_state_schema >= 3`; a consumer MUST branch on the
  version, because on an older record `false` means "not recorded", not "lock-built". The
  human-readable `info` now prints `locked <rev> (overridden: built from the local clone)` so the
  annotation cannot mislead in the opposite direction.
- **The unresolvable-rev warning is now conditional.** `markApplied` announces an empty
  `locked_revs` entry only when the input was NOT overridden. Its claim — "a `pn:applied` gate on this
  repo stays blocked" — is false for an overridden input, and warning anyway would fire on every
  apply, which is what teaches an operator to ignore warnings. The empty entry itself is still
  RECORDED either way.

The `markApplied` TOCTOU (bead `pg2-0782j`) is **not** addressed here and was the reason the operator
declined the alternative of comparing against the overridden eval-time HEAD: that is exactly the value
`markApplied` mis-samples. Recording WHICH inputs were overridden does not depend on any HEAD reading,
so it is unaffected.
