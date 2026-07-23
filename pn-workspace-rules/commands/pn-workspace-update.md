---
description: >-
  Update every repo in this pn-workspace by relocking its flake inputs (nixpkgs +
  third-party + workspace siblings) in an isolated coordinated workforest,
  validating the whole set builds, landing it, then pushing. ON SUCCESS THIS
  PUSHES EVERY REPO TO origin/main.
---

# /pn-workspace-update

You are running the **update** consumer of the workforest work-cycle. It relocks
every repo's flake inputs (nixpkgs + third-party + workspace siblings) in an
isolated coordinated set, validates and lands that set onto the local primary
branches, tears the set down, then publishes.

## Announce first (MUST)

Open by telling the user plainly, in one line, what this will do:

> This will relock every repo's flake inputs (nixpkgs + third-party + workspace
> siblings) in an isolated workforest, land it onto local `main`, and — on
> success — **push every repo to `origin/main`**. You invoked
> `/pn-workspace-update`; that invocation is the authorization to push. I will not
> ask again.

Do **not** add a second approval gate. A human ran this command; that IS the
authorization. (If a repo's `integrate-branch` strategy turns out to be
`pull-request`, landing will stop-and-report at that repo per `land-workforest`
— that is expected, not a failure to work around.)

## The pipeline

The read/build-heavy prefix (fork → update-relock → validate) runs in an isolated
subagent; the main session then lands, cleans up, and publishes. Stop and report
if any stage halts.

1. **Dispatch the runner.** Dispatch the subagent
   `pn-workspace-rules:pnwf-update-runner` via the Task tool with NO model
   override (its frontmatter pins Sonnet), passing the absolute `CANONICAL_ROOT`,
   `BRANCH = pn-workspace-update`, and any caveats the user gave this session. The
   runner does fork → update-relock → validate in isolation and returns a single
   strict-JSON status line.
2. **Handle the runner's JSON status.**
   - **`gate` / `fork` / `resume-vs-discard`** → decide WITH the user per
     `fork-workforest` step 3 (resume the existing set, or discard + re-fork),
     then continue the SAME runner (send it the decision) — its context is
     preserved.
   - Unlike `/pn-workspace-sync`, there is **NO** `sync-fetch` / `rebase-conflict`
     gate: `/pn-workspace-update` relocks rather than fetch+rebase, so the runner
     never emits a rebase-conflict gate. The only gate is `fork` /
     `resume-vs-discard` above.
   - **`halt`** → surface the reason and STOP; do NOT work around it. Reasons
     include `update-failed`, `incomplete-update`, `validate-failed`, or a
     canonical anomaly (R-3/R-8).
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

- **Prefix vs. main session.** The `pnwf-update-runner` subagent offloads only the
  read/build-heavy prefix (fork → update-relock → validate). Land, cleanup, and
  publish stay in the main session because they are shell-state-sensitive
  (`integrate-branch` needs a persistent cwd + shell vars) and irreversible — a
  subagent's Bash calls do not persist cwd/env between calls.
- The spine (fork → update-relock → validate → land → cleanup) performs no remote
  writes on the `ff-merge-to-main` path; the POST steps are the deliberate,
  invocation-authorized pushes. `pnwf update-relock` itself refuses if any member
  branch has an upstream, so the in-set relock cannot write to a remote. Your
  invocation of `/pn-workspace-update` is itself the authorization — do NOT re-ask
  before publishing.
- If any stage stops (e.g. a `pull-request` repo or a canonical anomaly), stop the
  whole run and report per that stage's guidance — do not push a partially-landed
  workspace.
- **Ordering precondition.** `/pn-workspace-update` does NOT fetch or rebase onto
  `origin` first — it only relocks. It assumes `origin/main` is fast-forwardable
  from local `main`. Run `/pn-workspace-sync` FIRST to converge with `origin`. If
  the POST `pn workspace push` is rejected (non-fast-forward — `origin` advanced),
  STOP AND REPORT: the relock has already landed on local `main` but publish is
  deferred; recovery is to run `/pn-workspace-sync`, then re-publish. You MUST NOT
  force-push.
- **Why the POST `--siblings-only` pass is still needed.** The in-set relock could
  not converge sibling flake inputs to pushed tips — nothing is pushed in-set (the
  set validates via `--override-input`), so no sibling has a published tip to
  relock against. The POST `pn workspace update --siblings-only` pass is what
  actually relocks siblings to the freshly-pushed local-`main` tips. It inherits
  sync's dependence on the (tracked, pg2-j2f8f) push-between-repos behavior of
  `--siblings-only`.
- **In-set update-phase hooks.** `pn workspace update --in-place` (which
  `pnwf update-relock` runs inside the set) fires each repo's `post-update` hooks
  (e.g. install-pre-commit-hooks). These warn-but-do-not-abort and only touch a
  gitignored symlink, so they are a safe no-op for landing.
- **Known limitation.** An ADR-0020 "silently transient" relock step can leave a
  repo green while an update was skipped; this run reports `done` regardless
  (inherited from `pn workspace update`).
