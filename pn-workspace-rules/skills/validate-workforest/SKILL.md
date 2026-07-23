---
name: validate-workforest
description: >-
  Use to VALIDATE a coordinated workforest set before landing it — confirm the
  isolated cross-repo workspace still builds/checks cleanly after the WORK, so
  landing is safe. Fires on: "validate the workforest", "is this set good to
  land?", the validate step of `/pn-workspace-sync` or `/pn-workspace-update`, and the check-before-land
  point of the ad-hoc bead work-cycle. This is the VALIDATE stage of the
  workforest work-cycle (fork → WORK+validate → land → cleanup). It checks the
  WORKSPACE (does the assembled system still build), NOT whether the WORK
  achieved its goal. Do NOT use for a single repo's own tests, to land the set
  (`land-workforest`), or as the workspace Completion Gate outside a set (that is
  the pn-workspace-rules Completion Gate directly).
---

# validate-workforest

**RUN FROM: inside the set** (`<workspace_root>/.workforests/<branch>`). This
stage MUST refuse to run when the resolved root is the canonical workspace
rather than a `.workforests/<branch>` set — run `pnwf resolve` and, if
`in_workforest` is false (or `pnwf` exits non-zero), **halt and report**.

**Contract (MUST).** On success, the **workspace is guaranteed valid** — i.e.
the coordinated set still builds/checks cleanly and is safe to land. This stage
validates the _workspace_, not whether the WORK was correct; a green validate
does not mean the change did what it intended.

**Disambiguation.** This validates a whole coordinated SET. It is the in-set
form of the pn-workspace Completion Gate; do not confuse it with a single repo's
unit tests.

## Deterministic facts from `pnwf`; tier judgment stays here

`pnwf` supplies facts to inform the tier: `pnwf repos` (the set's members in
topo order) and, per repo, whether it changed vs its primary (via the guarded
primitives). This skill owns the judgment the script cannot make: **which
Completion-Gate tier** actually guarantees the whole set is valid.

## Steps

1. **Location guard (MUST).** `pnwf resolve`; require `in_workforest = true`.
   Any non-zero `pnwf` exit → halt and report.
2. **Choose the tier.** Apply the existing **pn-workspace-rules Completion Gate**
   tiering (do NOT restate the checklist here — follow that skill's tier table).
   Because "does the change touch the assembled system" is often not
   script-decidable, **default to the full `pn workspace build` (Tier 3)**. Go
   lighter only when a lower tier still guarantees the _whole set_ is valid.
   - Note the `--repos` subset case: a subset that excludes the terminal cannot
     `pn workspace build`; validate at the highest tier the subset supports and
     say so in the report.
3. **Run the selected `pn` check verbs** for that tier (e.g. `pn workspace
flake-check`, or `pn workspace build`), then `pn workspace doctor` as the
   final consistency gate.
4. **Dirty tree → WARN, do not fail (MUST).** If the set has uncommitted changes,
   validate MUST NOT fail on that alone — it **warns** (the WORK may be
   mid-flight). Landing has its own no-uncommitted-changes precondition.

## Policies

- MUST guarantee validity on success.
- MUST NOT fail solely because the working tree is dirty — warn instead.
- This is the single Facade for validating a set; consumers (the sync command,
  the bead work-cycle) call it rather than re-deriving check commands.

## Relationship to landing

`validate-workforest` is a **pre-rebase snapshot**. `land-workforest` rebases
each repo onto the _current_ primary at land time, so validate SHOULD
immediately precede land. The post-land recheck is a `pn workspace build` on the
canonical primary (the set is dismantled during landing).
