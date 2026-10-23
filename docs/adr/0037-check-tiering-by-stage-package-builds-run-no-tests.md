# ADR-0037: Checks are tiered by stage; package builds run no tests and a full flake check is not a per-change or land-time gate

**Date:** 2026-10-23
**Status:** Accepted (supersedes 0021)
**Deciders:** phillipgreenii
**Tracking:** `pg2-3i12x` (the check/test tiering design), `pg2-pla9d` (program), `pg2-pla9d.26` (Phase 4 deferred items), `pg2-g5c4i` (this ADR)

## Context

ADR [0021](0021-subpackages-check-scoping-mkgotest.md) said how a Go module's test gate is built
(an unscoped `mkGoTest` check, because a `subPackages`-scoped package build only tests its pinned
entrypoint). It was written when a green package build, a green `nix flake check` and a landable
branch were treated as one thing, and it carried a second-hand copy of the check-phase behavior
it was working around.

On 2026-10-01 the operator ruled on the check/test tiering plan (design bead `pg2-3i12x`, rulings
R1 to R4) because landing took over an hour: a measured load average of 82/147/158 on 11 cores,
five or more concurrent `nix flake check` runs, and a full flake check mandated in up to four
places per change (wrap-up, the drain delegate gate, the land step FF-2a re-run on every retry,
and the Completion Gate wording). Those rulings were carried out as separate changes and amendments
(ADR 0008 and ADR 0021 amendments of 2026-10-01 for `doCheck`; the removal of FF-2a from the
`ff-merge-to-main` skill; `pg-hooks` and the pre-land steps). Nothing recorded the whole decision
in one place, and ADR 0021 still described a world with a default check phase.

On 2026-10-23 the operator walked the Phase 4 deferred items (bead `pg2-pla9d.26`) and ruled, for
item 6, verbatim: "i agree with this, but we shouldnt' bre ammending ADRs, but superceeding
them." This ADR is that supersession. The `doCheck` amendment already written into ADR
[0008](0008-adopt-gomod2nix-for-go-packages.md) on 2026-10-01 is left exactly as it is.

## Decision

### 1. Package builds run no tests

- `mkGoApp` and `mkGoBinary` MUST default to `doCheck = false` (operator ruling R1, 2026-10-01,
  bead `pg2-pla9d.2`), overriding gomod2nix's default of `true`. A green package build is NOT a
  test result.
- A package that genuinely relies on its own check phase MAY opt back in with `doCheck = true` and
  MUST say why at the use site. `mkGoBinary`'s `testDeps` only matter with `doCheck = true`.
- The `subPackages` scoping footgun analysed in ADR 0021 still applies to a build with
  `doCheck = true`; 0021 stays in the repo as the record of that analysis.

### 2. Tests live in `checks.*`, one unscoped check per Go module

These rules are carried forward from ADR 0021 as amended on 2026-10-01 (they are restated here
because 0021 is superseded, not because they changed):

- Every Go module with tests MUST have a dedicated test check, normally
  `goBuilders.mkGoTest` (`checks.<system>.<name>-go-tests`, for example `pn-go-tests` and
  `pg-go-mutate-tui-go-tests`). A module with no such check has its tests run nowhere under
  `nix flake check`.
- `mkGoTest` MUST set no `subPackages`, MUST strip `-trimpath` from `GOFLAGS` in its `buildPhase`,
  and runs `go vet`.
- `subPackages = [ "cmd/x" ]` on the package derivation MAY (and typically SHOULD) stay, to pin the
  shipped `bin/`. It controls what ships; `mkGoTest` controls what is tested.
- A consumer that cannot reach base's `mkGoTest` MAY use a `mkGoApp` check derivation built over
  the whole module without `subPackages`, and that derivation MUST pass `doCheck = true`.
- A build-tagged suite MUST have its own check (ADR [0024](0024-build-tagged-suites-need-their-own-check.md),
  which extended 0021 and stays in force; read its references to 0021 as references to this rule).

### 3. A full `nix flake check` is not a per-change or land-time gate

- A full `nix flake check` MUST NOT be a mandatory step of a per-change workflow or of landing
  (operator ruling 2026-10-01, which dropped the land step FF-2a). An agent MAY always run more
  checks than the workflow demands, and SHOULD build the targeted
  `nix build .#checks.<system>.<name>` checks relevant to a change that touched shared
  infrastructure, in the background. That is the author's judgment, not a gate.
- The accepted interim risk: a workspace repo with no cloud CI has, as its only automatic runners,
  the commit-time `run-unit-tests` hook (touched projects only), the pre-land hook run over the
  branch diff, and where opted in the eval-only check below. A failure that only a full check would
  have found surfaces at `pn workspace apply`, in post-land or CI, and is fixed forward. Branch
  protection MUST NOT be added to compensate (workspace policy, 2026-07-15).
- The land gates of `ff-merge-to-main` are exactly: FF-1b, the consolidated hook run over the
  branch diff (`pg-hooks run pre-land`), and FF-1c, the eval-only `pre-land-eval` check (design
  section 9, built after the 2026-10-23 ruling on `pg2-pla9d.26`; see
  [docs/hooks.md](../hooks.md#the-eval-only-pre-land-check-pre-land-eval)), enabled per clone by
  `git config pgii-integrate-branch.preLandEval true` and a no-op elsewhere. Routing the sibling
  input overrides through `pn workspace flake-check --no-build` is bead `pg2-8uc2d`, a separate
  experiment.
- Slow nix work MUST NOT run in a git hook (the hook policy HK-2, unchanged).

### 4. Stages are named, never numbered

New text MUST name a check by its stage (commit-time fix, pre-commit hooks, pre-land, post-land,
apply, CI) and MUST NOT call it "Tier N": that label already has at least four incompatible
meanings (the `pn-workspace-rules` Completion Gate, the `pg-test-runner` spec, ADR 0022, ZR ADR
0050).

### 5. Deferred and dropped parts of the design (operator, 2026-10-01 and 2026-10-23)

- DEFERRED, not decided: post-land async runners (`pg2-na2nr`); the `nix-slot` machine-wide
  semaphore (ruling R3 and the 2026-10-23 "keep deferred", to be built only if the p99 tail of
  wrapped nix runs, about 3550 s over 7 days on 2026-10-22, is reported as a problem).
- DROPPED on 2026-10-23, with no bead: the `doInstallCheck` smoke-test ratchet (design section 5)
  and the success measures including the `found-at:*` labels (design section 13).
- An agent MUST NOT revive a dropped item as a new requirement without a fresh operator ruling.

## Consequences

### Positive

- One page records the whole tiering decision; a reader no longer assembles it from two
  amendments, a skill and a design bead.
- ADR 0021's status line says it is superseded, so nobody applies its pre-2026-10-01 text (default
  check phase, "gated by luck") as current.
- The supersession convention of the operator (supersede, never amend) is followed: no accepted
  ADR body was edited for this change.

### Negative / Neutral

- ADR 0008 still carries its 2026-10-01 amendment text for `doCheck` (left as it is by the
  operator's ruling), and ADR 0024 still says it amends 0021. The index annotates 0021 only.
- Code comments that cite "ADR 0021" (`lib/go-builders.nix`, `flake.nix`) name a rule restated
  here; they stay accurate and were not edited.
- Whole-repo failures that only a full check finds are detected later than before; this is the
  operator's accepted risk, mitigated only by author judgment and by the deferred post-land
  runners.

## Alternatives Considered

- **Amend ADR 0021 again.** Rejected by the operator on 2026-10-23: accepted ADRs are superseded,
  not amended, and 0021 already carried one amendment that contradicted half of its own rules.
- **Fold the decision into ADR 0008.** Rejected: 0008 is the gomod2nix adoption decision, and the
  land and stage rules are not Go-specific.
- **Supersede 0008's `doCheck` amendment too.** Rejected for now: the operator ruled it stays as
  written; this ADR restates the rule as the authority without editing 0008.

## Related Decisions

- Supersedes ADR [0021](0021-subpackages-check-scoping-mkgotest.md).
- ADR [0008](0008-adopt-gomod2nix-for-go-packages.md) — the `doCheck = false` amendment of
  2026-10-01 is the implementation record of section 1; left unedited.
- ADR [0024](0024-build-tagged-suites-need-their-own-check.md) — build-tagged suites; stays in force.
- ADR [0032](0032-per-clone-hook-bundle-replaces-generated-config-and-shim.md) — the hook bundle
  that `pg-hooks run pre-land` (FF-1b) runs.
- Design bead `pg2-3i12x` (check/test tiering, draft 4.1) and the operator rulings on
  `pg2-pla9d.26` (2026-10-23).
