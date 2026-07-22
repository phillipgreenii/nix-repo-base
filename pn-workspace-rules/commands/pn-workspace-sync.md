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

The read/build-heavy prefix (fork → sync-fetch → validate) runs in an isolated
subagent; the main session then lands, cleans up, and publishes. Stop and report
if any stage halts.

1. **Dispatch the runner.** Dispatch the subagent
   `pn-workspace-rules:pnwf-runner` via the Task tool with NO model override (its
   frontmatter pins Sonnet), passing the absolute `CANONICAL_ROOT`,
   `BRANCH = pn-workspace-sync`, and any caveats the user gave this session. The
   runner does fork → sync-fetch → validate in isolation and returns a single
   strict-JSON status line.
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
     anomaly (R-3/R-8) or a broken validate.
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
   - `pn workspace update --siblings-only` — relocks the workspace-sibling flake
     inputs (this **pushes** each repo's `HEAD:main`; a sibling must be pushed
     before consumers relock — tracked: pg2-j2f8f).
   - `pn workspace push` — the catch-all publish.

## Notes

- **Prefix vs. main session.** The `pnwf-runner` subagent offloads only the
  read/build-heavy prefix (fork → sync-fetch → validate). Land, cleanup, and
  publish stay in the main session because they are shell-state-sensitive
  (`integrate-branch` needs a persistent cwd + shell vars) and irreversible — a
  subagent's Bash calls do not persist cwd/env between calls.
- The spine (fork → sync-fetch → validate → land → cleanup) performs no remote
  writes on the `ff-merge-to-main` path; the POST steps are the deliberate,
  invocation-authorized pushes. Your invocation of `/pn-workspace-sync` is itself
  the authorization — do NOT re-ask before publishing.
- If any stage stops (e.g. a `pull-request` repo, an unresolved conflict, or a
  canonical anomaly), stop the whole run and report per that stage's guidance —
  do not push a partially-landed workspace.
