---
disable-model-invocation: true
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

The read/build-heavy prefix (fork → sync-fetch → validate) runs in an isolated
subagent; the main session then lands, cleans up, and publishes. Stop and report
if any stage halts.

1. **Dispatch the runner.** Dispatch the subagent
   `pn-workspace-rules:pnwf-runner` via the Task tool with NO model override (its
   frontmatter pins Sonnet), passing the absolute `CANONICAL_ROOT`,
   `BRANCH = pn-workspace-sync`, and any caveats the user gave this session. The
   runner does fork → sync-fetch → validate in isolation and returns a single
   strict-JSON status line.

   **The brief MUST NOT offer the runner `run_in_background` (MUST).** The
   standing long-command guidance — "set an explicit timeout **or** run with
   `run_in_background` and watch it with Monitor" — is written for THIS session,
   which survives to receive the completion notification. A subagent does not, so
   a backgrounded stage is torn down mid-write: no JSON status line, prose in its
   place, and a part-rebased set someone must disposition (bd `pg2-es5nn`,
   observed on the sibling `pnwf-update-runner`). Forward the **timeout half
   only**; the runner's own R2 pins `600000` ms for its long steps. If the brief
   restates the long-command rule, it MUST state that the background option is
   withheld for a subagent.

2. **Handle the runner's JSON status.**
   - **`gate` / `fork` / `resume-vs-discard`** → decide WITH the user per
     `fork-workforest` step 3 (resume the existing set, or discard + re-fork),
     then continue the SAME runner (send it the decision) — its context is
     preserved.
   - **`gate` / `sync-fetch` / `rebase-conflict`** → conflicts are the EXPECTED
     case for sync. Resolve the conflict WITH the user in the reported worktree,
     run `git -C <path> rebase --continue`, then continue the SAME runner (it
     re-runs `pnwf sync-fetch`).
   - **`halt`** → surface the reason and STOP; do NOT work around a canonical
     anomaly (R-3/R-8) or a broken validate. Reasons are `fetch-failed`,
     `incomplete-sync`, `validate-failed`, or a `fork` reason line.
   - **`halt` / `sync-fetch` / `incomplete-sync`** → the runner's turn ended
     before the fetch+rebase finished. The halt carries a `dirty` array (read it
     as `.dirty // []`) naming each dirty or `mid_rebase` member and its file
     paths; surface those verbatim and name the recovery the user can then
     authorize — disposition the named residue, then re-run
     `pnwf sync-fetch --set` in the set (THIS session may background that; it
     survives to receive the notification). Treat it as `incomplete-sync`, NOT as
     a `rebase-conflict` gate: no conflict was observed, so `rebase --continue` is
     not automatically the right move. A missing/prose response where the JSON
     line belongs is the same class of failure — probe the set rather than
     assuming the stage finished.
   - **`done`** → proceed to the main-session landing stages below.
   - If `model_env` is not `unset`/`sonnet`, WARN the user before continuing
     (silent-Opus guard: an env override may have forced a non-Sonnet model).
3. **`land-workforest`** — invoke the Skill in the main session (cwd persists
   here, so `integrate-branch` works as authored). It lands each repo in topo
   order, stop-on-blocked; handle its outcomes per that skill (`landed` / nothing
   to land / `pr-opened`/`pr-updated` / `stopped:<reason>` → stop-and-report as
   it specifies).
4. **`cleanup-workforest`** — invoke the Skill in the main session.
5. **POST — publish (main session):**
   - `pn workspace push` — the ONE publish step. It walks the repos in topological
     order and, per repo, relocks that repo's workspace-sibling flake inputs
     against their upstreams' current remote tips (committing any bump), then
     pushes. Because dependencies are pushed first, a consumer relocks onto the
     tips just published in this same run — which is why publishing and relocking
     are interleaved in one command rather than split across two (ADR 0023).
   - There is **no** `pn workspace update --siblings-only` step here any more.
     `update` is local-only and does not push, so it can no longer publish
     anything (ADR 0023, beads pg2-j2f8f / pg2-x42j3). Do not re-add it.
   - If a repo has uncommitted changes, `push` refuses to relock it and STOPS
     (the relock ends in a commit). Report the named repo; the user then commits or
     stashes, or authorizes `pn workspace push --no-siblings` to publish without
     propagating locks.
   - **This step is what discharges validate's `flake-lock-fresh` warnings.**
     `sync-fetch` advances sibling HEADs, so consumer locks go stale by
     construction; validate warns rather than halting on exactly that drift
     (`validate-workforest` step 5, bd `pg2-1i1ev`) because only a published rev can
     be pinned and nothing in-set can converge it. The relaxation is therefore
     CONDITIONAL on this step running: if publishing is ever removed, reordered
     before validate, or routinely run as `--no-siblings`, that carve-out becomes a
     silent hole and MUST be revisited with it.

## Notes

- **Prefix vs. main session.** The `pnwf-runner` subagent offloads only the
  read/build-heavy prefix (fork → sync-fetch → validate). Land, cleanup, and
  publish stay in the main session because they are shell-state-sensitive
  (`integrate-branch` needs a persistent cwd + shell vars) and irreversible — a
  subagent's Bash calls do not persist cwd/env between calls, and a subagent
  cannot await its own background job across the end of its turn (only this
  session receives that notification), which is why the runner runs every stage in
  the foreground.
- The spine (fork → sync-fetch → validate → land → cleanup) performs no remote
  writes on the `ff-merge-to-main` path; the POST step is the deliberate,
  invocation-authorized push. Your invocation of `/pn-workspace-sync` is itself
  the authorization — do NOT re-ask before publishing.
- If any stage stops (e.g. a `pull-request` repo, an unresolved conflict, or a
  canonical anomaly), stop the whole run and report per that stage's guidance —
  do not push a partially-landed workspace.
