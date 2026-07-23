---
name: cleanup-workforest
description: >-
  Use to TEAR DOWN a coordinated workforest set after landing — best-effort
  removal of the landed repos' worktrees and branches, preserving anything not
  yet landed. Fires on: "clean up the workforest", "remove the set", "tear down
  the workforest after landing", the cleanup step of `/pn-workspace-sync` or `/pn-workspace-update`, and
  the final step of the ad-hoc bead work-cycle. This is the CLEANUP stage of the
  workforest work-cycle (fork → WORK+validate → land → cleanup). It removes only
  what has actually landed and keeps un-landed / pull-request repos with a
  report. Do NOT use it to land work (`land-workforest`), and do NOT use it as a
  force-delete of unmerged work — it deliberately preserves un-landed branches
  unless you pass the explicit force flags.
---

# cleanup-workforest

**RUN FROM: the canonical workspace root.** This stage MUST `cd` to the
canonical root **before** removing the set (removing a set you are standing
inside is unsafe). If `pnwf resolve` shows you are inside the set, `cd` to its
`canonical_root` first.

**Purpose.** Best-effort teardown after landing: remove the worktrees + branches
of repos that actually landed, and **keep** (with a report) any repo that did
not — including `pull-request` repos and un-landed clean work.

**Disambiguation.** This tears down a whole coordinated SET. It is not a landing
step and not a force-delete; by default it never discards unmerged work.

## Deterministic teardown in `pnwf`; the skill wraps it

The teardown is `pnwf cleanup <branch> [--force-dirty-worktree-removal]
[--force-unlanded-branch-removal]`. This skill wraps it so you can act on
anything the tool refuses.

## The landed-test (MUST understand)

`pnwf cleanup` classifies each repo from the canonical clone on its primary:

1. **Branch absent** (a prior `integrate-branch` FF-4 already deleted it) →
   landed, nothing to do.
2. Otherwise `git merge-base --is-ancestor <branch> <primary>`, distinguishing
   **exit 0** (landed → remove worktree + `git branch -d`) from **exit 1** (not
   landed → keep) from **exit 128** (absent ref → handled as (1)). It **NEVER**
   uses `git branch -d` _as the test_ — `git branch -d`'s upstream-aware "merged"
   check would delete an open PR's branch.
3. **Not landed** (incl. `pull-request` repos and un-landed clean work) → keep
   the worktree + branch and report it.

## Behavior

- **Best-effort (MUST):** `pnwf cleanup` processes every repo, removes what is
  removable, reports the rest, and never aborts on one un-removable repo. Treat a
  non-zero `pnwf` exit as information to report/act on, not as a reason to stop
  mid-teardown.
- The per-repo `git worktree remove` / `git branch -d` loop is primary;
  `pn workspace workforest remove` deletes the emptied set directory **only when
  no worktree is kept**. When any worktree is kept, the set directory is left in
  place and reported.
- **Force flags** (only when the operator intends to discard):
  `--force-dirty-worktree-removal` forces removal of a dirty worktree;
  `--force-unlanded-branch-removal` removes the worktree and `git branch -D`s a
  **non-landed** branch. The report names these flags for any kept repo so the
  operator can choose.

## Judgment the skill owns

For any repo the tool **keeps**, decide with the operator what to do rather than
reflexively forcing: e.g. **finish landing** a kept repo (re-run
`land-workforest`, or complete its PR) is usually preferable to force-discarding
its branch. Only pass a force flag when discarding that repo's work is the
intended outcome.

## After cleanup

If every repo landed, the set directory is gone. Validate the final result with
a `pn workspace build` (or the matching Completion-Gate tier) on the canonical
primary. If any repo was kept, the set remains for retry/inspection — report
which repos are kept and why.
