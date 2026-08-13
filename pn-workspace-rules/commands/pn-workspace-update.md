---
disable-model-invocation: true
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

   **The brief MUST NOT offer the runner `run_in_background` (MUST).** The
   standing long-command guidance — "set an explicit timeout **or** run with
   `run_in_background` and watch it with Monitor" — is written for THIS session,
   which survives to receive the completion notification. A subagent does not, so
   a backgrounded relock is torn down mid-write: no JSON status line, prose in its
   place, and a half-relocked set whose own pre-flight then refuses the re-run
   (bd `pg2-es5nn`). Forward the **timeout half only**; the runner's own R2 pins
   `600000` ms for its long steps. If the brief restates the long-command rule,
   it MUST state that the background option is withheld for a subagent.

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
   - **`halt` / `update` (either `update-failed` or `incomplete-update`)** → the
     halt carries a `dirty` array (read it as `.dirty // []`) naming each dirty
     member and its file paths. Surface those paths verbatim in your
     stop-and-report, and name the recovery the user can then authorize:
     disposition the named residue (a relock leaves regenerated lock churn, but
     that is only knowable after inspection), then re-run
     `pnwf update-relock --set` in the set. You MUST NOT re-dispatch the runner
     first — `update-relock`'s pre-flight refuses a dirty member, so the re-run
     would fail on the residue. A missing/prose response where the JSON line
     belongs is itself this class of failure: treat it as an
     `incomplete-update` halt and run the residue probe yourself rather than
     assuming the relock finished.
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
     `update-relock` commits a lock bump per member, so consumer locks go stale by
     construction; validate warns rather than halting on exactly that drift
     (`validate-workforest` step 5, bd `pg2-1i1ev`) because only a published rev can
     be pinned and nothing in-set can converge it. The relaxation is therefore
     CONDITIONAL on this step running: if publishing is ever removed, reordered
     before validate, or routinely run as `--no-siblings`, that carve-out becomes a
     silent hole and MUST be revisited with it.

## Notes

- **Prefix vs. main session.** The `pnwf-update-runner` subagent offloads only the
  read/build-heavy prefix (fork → update-relock → validate). Land, cleanup, and
  publish stay in the main session because they are shell-state-sensitive
  (`integrate-branch` needs a persistent cwd + shell vars) and irreversible — a
  subagent's Bash calls do not persist cwd/env between calls, and a subagent
  cannot await its own background job across the end of its turn (only this
  session receives that notification), which is why the runner runs every stage in
  the foreground.
- The spine (fork → update-relock → validate → land → cleanup) performs no remote
  writes on the `ff-merge-to-main` path; the POST step is the deliberate,
  invocation-authorized push. `pnwf update-relock` itself refuses if any member
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
- **Why sibling convergence only happens in the POST step.** The in-set relock
  cannot converge sibling flake inputs to pushed tips — nothing is pushed in-set
  (the set validates via `--override-input`), so no sibling has a published tip to
  relock against. Nor can the post-land `pn workspace update` do it: a lock can
  only pin a rev that is already on the remote, and update no longer pushes. So
  `pn workspace push` is the only step that can converge them, and it does —
  push a repo, relock its consumers onto that new tip, push those, in topological
  order (ADR 0023, beads pg2-j2f8f / pg2-x42j3).
- **In-set update-phase hooks.** `pn workspace update --in-place` (which
  `pnwf update-relock` runs inside the set) fires each repo's `post-update` hooks
  (e.g. install-pre-commit-hooks). These warn-but-do-not-abort and only touch a
  gitignored symlink, so they are a safe no-op for landing.
- **Known limitation.** An ADR-0020 "silently transient" relock step can leave a
  repo green while an update was skipped; this run reports `done` regardless
  (inherited from `pn workspace update`).
