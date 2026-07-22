---
description: >-
  Sync every repo in this pn-workspace with its remote by doing the fetch +
  rebase in an isolated coordinated workforest, landing it, then pushing. ON
  SUCCESS THIS PUSHES EVERY REPO TO origin/main.
---

# /pn-workspace-sync

You are running the **sync** consumer of the workforest work-cycle. It fetches
each repo's remote changes and rebases the workspace onto them in an isolated
coordinated set, validates and lands that set onto the local primary branches,
tears the set down, then publishes.

## Announce first (MUST)

Open by telling the user plainly, in one line, what this will do:

> This will fetch + rebase every repo in the workspace in an isolated workforest,
> land it onto local `main`, and — on success — **push every repo to
> `origin/main`**. You invoked `/pn-workspace-sync`; that invocation is the
> authorization to push. I will not ask again.

Do **not** add a second approval gate. A human ran this command; that IS the
authorization. (If a repo's `integrate-branch` strategy turns out to be
`pull-request`, landing will stop-and-report at that repo per `land-workforest`
— that is expected, not a failure to work around.)

## The pipeline

Run the stages in order; each is a skill — invoke it via the Skill tool. Stop
and report if any stage halts.

1. **`fork-workforest`** on the fixed single-segment branch **`pn-workspace-sync`**
   (from the canonical root).
2. **WORK — `pnwf sync-fetch`** (inside the set): it runs `git fetch origin` then
   rebases each member onto its remote primary. Use `pnwf sync-fetch`, NOT a bare
   `pn workspace rebase` (which skips no-upstream branches) nor
   `git rebase origin/main` alone (which does no fetch). **Conflicts are the
   EXPECTED case for sync** — on a stop, resolve the conflict in the named
   worktree, `git -C <path> rebase --continue`, then re-run `pnwf sync-fetch`
   until it passes cleanly.
3. **`validate-workforest`** (inside the set).
4. **`land-workforest`** (inside the set) — lands each repo in topo order via
   `integrate-branch`, stop-on-blocked.
5. **`cleanup-workforest`** (from the canonical root).
6. **POST — publish (from the canonical root):**
   - `pn workspace update --siblings-only` — relocks the workspace-sibling flake
     inputs (this **pushes** each repo's `HEAD:main`; a sibling must be pushed
     before consumers relock — tracked: pg2-j2f8f).
   - `pn workspace push` — the catch-all publish.

## Notes

- The spine (fork → sync-fetch → validate → land → cleanup) performs no remote
  writes on the `ff-merge-to-main` path; the POST steps are the deliberate,
  invocation-authorized pushes.
- If any stage stops (e.g. a `pull-request` repo, an unresolved conflict, or a
  canonical anomaly), stop the whole run and report per that stage's guidance —
  do not push a partially-landed workspace.
