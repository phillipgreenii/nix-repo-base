# pn workspace update — per-repo worktree isolation — Design

**Status:** Draft — pending review
**Date:** 2026-06-24
**Repos affected:** `phillipg-nix-repo-base` (`modules/pn`, docs, agent-rules); audit of each consumer repo's `update-locks.sh`
**Related:** 2026-06-16 coordinated-worktrees design (the _set_ model — explicitly NOT reused here; see "Why not the coordinated set"), ADR 0002 (pn-workspace.toml schema)

> **Historical note (pg2-f1k1):** the `pn-workspace.revs.json` / `RevLock` rewrite
> described below was removed as write-only dead code in bead `pg2-f1k1`. The
> worktree-isolation flow is otherwise unchanged; `flake.lock` remains the source of
> truth for the pinned remote revs. `revs.json` references here are retained for
> historical accuracy.

## Problem

`pn workspace update` runs, per repo in topological order, directly on the
primary checkout's `main`: `git pull --rebase --autostash` → `./update-locks.sh`
(which runs `nix flake update`, bumps package hashes, and commits per step) →
`git push` (`internal/workspace/update.go:82`). This relocks each repo against
the **remote** commit of its upstream siblings — consumer flakes pin
`github:phillipgreenii/nix-repo-base` and friends, and the lock records the
remote rev (verified: `nix-personal/flake.lock` pins repo-base
`rev a5b0cbf8…`, identical to `pn-workspace.revs.json`). Topological order plus
push-before-downstream is therefore load-bearing: an upstream must be pushed
before a downstream's `nix flake update` can pick up its new remote commit.

The whole sweep is slow — `nix flake update` + hash bumps + `nix fmt` +
per-step commits across ~6 repos, with nix evaluation and network fetches. While
it runs, it owns the primary checkouts: each repo's `main`, working tree, and
index are mutated in place, so the canonical clones are unusable for other work
until the sweep finishes.

**Goal (Phillip):** keep the long, churny part off the primary checkouts so the
canonical clones and their `main` branches stay free for other work, and only
touch the primary `main` with a fast fast-forward at the very end. The work
should happen in a throwaway git worktree per repo and be rebased / fast-forward
merged back into the primary `main`.

## Why not the coordinated worktree set

The 2026-06-16 coordinated-set model (`pn workspace worktree add`) builds one
directory that is a complete workspace whose repos are worktrees on a shared
branch, and relocks via local `--override-input git+file://{set}/{repo}` paths.
That is wrong for _update_: it would lock each repo against its **local sibling
worktree path**, not the **remote commit** the real lock must record. Update's
correctness depends on relocking against pushed remote revs, so this design uses
**independent, ephemeral, per-repo worktrees** — no set, no local overrides,
each repo relocked against real remote siblings exactly as today.

## Decisions (resolved with Phillip, 2026-06-24)

1. **Default flips.** `pn workspace update` uses the worktree-isolated flow by
   default. `--in-place` restores today's direct-on-`main` behavior unchanged.
2. **Per-repo, sequential, topological order.** One ephemeral worktree + branch
   per repo, created → used → integrated → destroyed before the next repo. No
   coordinated set.
3. **Foreground only.** Isolation (free canonical clones) is the entire win; no
   background/daemon/detach machinery.
4. **`upgrade` = worktree-update then `apply`.** Because integration lands the
   new locks on the primary `main` in place, `apply` (`darwin-rebuild switch`,
   which cannot be isolated) runs against the primary exactly as today. `upgrade`
   inherits isolation for its update phase and also honors `--in-place`.
5. **Smart integration** (details below): fast-forward `main` whether or not it
   is the checked-out branch, without disturbing in-progress work in the primary;
   defer only when `main` itself is checked out and dirty.
6. **Worktree location** `{root}/.worktrees/.pn-update/<repo>-<run-ts>`; **branch**
   `pn-update/<run-ts>` (one timestamp per `update` invocation, shared as the
   branch name across all repos — each in its own repo). `<run-ts>` is
   high-resolution and PID-tagged (a sub-second timestamp + PID), so distinct
   invocations get distinct branch names and worktree dirs and never collide at
   `git worktree add -b`. **Concurrent `pn workspace update` runs in the same
   workspace are still not coordinated**: they do not collide on the
   branch/worktree, but both push to remote `main`, so the second run to reach a
   given repo's step-6 push has that push rejected (non-fast-forward) and that
   repo fails — run updates serially.
7. **Cascade = just continue.** On a per-repo failure, keep processing remaining
   repos and aggregate failures (today's behavior). Downstream repos that relock
   against an un-bumped upstream are the user's to reconcile from the per-repo
   outcomes; no automatic skip or cascade analysis.
8. **Leave-on-any-failure, remove-only-on-success.** Any failed step leaves that
   repo's worktree + branch in place for inspection / manual resume; the worktree
   and branch are removed only after a fully successful integration.

## Goals

1. Run the existing `update-locks.sh` work for each repo inside a throwaway
   worktree on a fresh branch, so the primary checkout's `main`, index, and
   working tree are untouched during the long phase.
2. Relock against **remote** siblings (unchanged correctness): topological order,
   push-before-downstream.
3. Integrate each repo's result back onto the primary `main` with a
   fast-forward, robust to the primary being checked out elsewhere.
4. Resilient per-repo failure handling with a clear end-of-run summary naming
   each repo's outcome and the step any failure stopped at.
5. Reuse the existing `Update` scaffolding (topo order, `revs.json` rewrite,
   JSONL eventlog, terminal requirement, failure aggregation) — the new code is
   the per-repo body plus worktree lifecycle, not a rewrite of the outer loop.
6. Update the supporting helpers, the consumer `update-locks.sh` scripts (audit),
   the docs/agent-rules, and `--help`.

## Non-Goals

- Background/detached execution; progress daemons; a status/attach command.
- Reusing or extending the coordinated-set (`pn workspace worktree`) model.
- Parallelizing repos (topological order is sequential by construction).
- Changing `update-locks.sh`'s internal contract (clean-tree gate, per-step
  commit/rollback, dev-shell re-exec, cache stamps) — it runs unchanged, just in
  a worktree directory.
- Cross-repo PR orchestration or remote-side merge policy beyond pushing `main`.

## Design

### Per-repo algorithm

Before the loop (reusing the in-place preamble): call `requireTerminal` first, so
a missing terminal errors _before_ any worktree is created; then resolve a single
`run-ts` and `UL_LIB_DIR` once (`ResolveULLibDir`, reused — a `""` result is a
hard error, per B1). Then for each repo `R` in topological order (`topoAlpha`,
dependencies first):

Let `P = {root}/R` (primary checkout) and
`W = {root}/.worktrees/.pn-update/R-<run-ts>` (ephemeral worktree),
`B = pn-update/<run-ts>` (branch).

1. **Create worktree + branch off local `main`.**
   `git -C P worktree add -b B W main`
   _(fail → record, continue to next repo.)_

2. **Sync the branch to remote `main`.**
   `git -C W fetch origin` then `git -C W rebase origin/main`.
   _(rebase conflict → `git -C W rebase --abort`; leave W+B; record; continue.)_

3. **Run the existing update in the worktree.**
   `./update-locks.sh` with `Dir=W` and env `PN_WORKSPACE_ROOT=root` and
   `UL_LIB_DIR=<resolved>` (resolved once before the loop; **must be non-empty** —
   see "Why … works unchanged"). The script itself recomputes and overwrites
   `WORKSPACE_ROOT=SCRIPT_DIR/..`, so any `WORKSPACE_ROOT` `pn` passes is inert.
   Commits land on `B` in `W`. `update-locks.sh` keeps its own clean-tree gate
   and rollback semantics (a freshly created worktree is clean by construction).
   _(non-zero exit → leave W+B; record; continue.)_

4. **Catch local-`main` drift — rebase onto local `main` FIRST.** During the
   long step-3 run you are free to work in the primary clone and may have
   committed to its `main` (commits that are not yet pushed). Rebase the branch
   onto the _current_ local `main` before touching the remote, so those unpushed
   local commits are carried into the result rather than lost:
   `git -C W rebase main`.
   _(conflict → `git -C W rebase --abort`; leave W+B; record; continue.)_

5. **Catch remote drift — re-fetch + rebase onto `origin/main`.**
   `git -C W fetch origin` then `git -C W rebase origin/main`.
   _(conflict → abort; leave W+B; record; continue.)_
   The **local-then-remote** order is deliberate (Phillip, 2026-06-24):
   incorporate unpushed local `main` commits (step 4) before layering on remote
   advances (step 5). After this, `B` carries local `main`'s unpushed commits +
   the latest `origin/main` + the lock commits.

6. **Publish — push the branch to remote `main` from the worktree.**
   `git -C W push origin HEAD:main` (a fast-forward push relative to
   `origin/main`; rejected only if the remote advanced in the race window between
   step 5 and here — treated as a failure).
   _(push fail → leave W+B; record; continue.)_

   > **Refinement of the literal "pull --rebase then push" step.** Phillip's
   > described step 5 was a pull-rebase + push on the primary. That only works
   > when the primary is checked out on `main`. To support the chosen _smart_
   > integration (primary may be on another branch), the remote-facing publish is
   > done from the worktree (`push HEAD:main`), which is checked out on `B`
   > regardless of the primary's state. The end state is identical: remote `main`
   > and local `main` both advance to `B`.

7. **Advance the local primary `main` (smart).** Determine the primary's state:
   - **On clean `main`:** `git -C P merge --ff-only B` — advances `main` and
     updates the working tree (fast, lock/`.nix` files only).
   - **On another branch** (`main` not checked out): `git -C P fetch . B:main` —
     fast-forwards the `main` ref only; the in-progress working tree on the other
     branch is untouched.
   - **On `main` but dirty:** **defer** — leave W+B, record
     `"integrate manually"`, continue.

   The fast-forward succeeds when local `main` is an ancestor of `B` — the common
   case, since step 4 rebased `B` onto local `main` and step 5 only layered the
   remote tip on top. **Genuine divergence is the exception and has a sharp edge:**
   if local `main` had unpushed commits _and_ `origin/main` advanced during the
   run, step 5 replays the local commits as new SHAs atop the moved remote tip, so
   by the time step 7 runs, **step 6 has already pushed `B` to remote `main`** and
   local `main` is no longer an ancestor → the ff fails. The end state is
   **asymmetric: remote `main` is advanced (and authoritative); local `main` is
   not, and it holds orphaned duplicate-SHA copies of commits now on the remote.**
   This is _not_ a "merge by hand" situation — `B`/remote is the truth. The repo
   **defers** (leave W+B, record) and the summary prints the correct recovery:
   reset local `main` to the pushed remote, **not** a merge —
   `git -C P fetch origin && git -C P branch -f main origin/main` (or, if `P` is
   checked out on `main`, `git -C P reset --hard origin/main`). The same
   asymmetric state can arise from a crash between steps 6 and 7; recovery is
   identical.

8. **On full success only:** capture `git -C P rev-parse HEAD` for the rev-lock,
   then `git -C P worktree remove W` and `git -C P branch -d B`.

After the loop: rewrite `pn-workspace.revs.json` at the primary root. Record the
rev each repo's downstream consumers will actually relock against — the **pushed
remote `main`** rev. For a cleanly-integrated repo that equals the new primary
`HEAD`; for a **pushed-but-deferred** repo (the step-7 divergence defer, where
step 6 already pushed) it is the pushed `B` / remote tip, **not** the stale local
`HEAD` — otherwise `revs.json` lags the remote that downstream repos lock against.
Repos that failed _before_ step 6's push keep their prior rev-lock entry. Then
emit the JSONL `run_start`/`project_result`/`run_end` events (with a per-project
`failed_step`); and print a summary table — one line per repo with outcome
(`ok` / `failed@<step>` / `deferred@integrate` / `skipped (dirty)`) and, for
non-`ok` repos, the worktree path + branch left behind and the one-liner to
resume.

### Why the existing `update-locks.sh` works unchanged in a worktree

- It re-execs into `nix develop "$SCRIPT_DIR"` and `cd`s to `$SCRIPT_DIR`; with
  `Dir=W`, `$SCRIPT_DIR=W`, so all flake work targets the worktree's flake.
- **The real `WORKSPACE_ROOT` / `UL_LIB_DIR` invariant** (corrected after review):
  each consumer `update-locks.sh` _unconditionally recomputes and exports_
  `WORKSPACE_ROOT="${SCRIPT_DIR}/.."`, so whatever `WORKSPACE_ROOT` `pn` passes is
  overwritten — under a worktree it becomes `{root}/.worktrees/.pn-update/`, not
  the workspace root. That is harmless **only because nothing reads
  `WORKSPACE_ROOT` at runtime except `determine-ul-lib-dir`**, which is invoked
  solely via `UL_LIB_DIR="${UL_LIB_DIR:-$(nix run …)}"`. So the load-bearing
  condition is: **`UL_LIB_DIR` must be injected non-empty** — then the resolver
  (the only `WORKSPACE_ROOT` consumer) is skipped and the clobbered value is
  inert. `ResolveULLibDir` is best-effort and returns `""` on failure; then
  `ulSubprocessEnv` omits `UL_LIB_DIR`, each consumer runs the resolver with the
  clobbered `WORKSPACE_ROOT`, the sibling probe misses (no repo-base under
  `.pn-update/`), and it falls through to the baked-in `UL_LIB_PACKAGE_PATH` —
  still correct, just a remote nix eval per repo. **The worktree flow must
  therefore treat a `""` `ResolveULLibDir` result as a hard error** (not silently
  take the slow path).
- Its clean-tree gate passes (fresh worktree), per-step commits land on `B`, and
  cache stamps under `.update-locks/steps/` arrive from `main` and behave exactly
  as in-place.

### CLI surface

- `pn workspace update [--in-place]` — default = worktree flow; `--in-place` =
  today's direct-on-`main` flow (the current `Update` body, preserved).
- `pn workspace upgrade [--in-place]` — update (worktree by default) then `apply`.
- `UpdateOptions` gains `InPlace bool`; `workspaceUpdateCmd` / `workspaceUpgradeCmd`
  register the flag (`internal/cli/workspace.go`). The existing in-place body is
  extracted to `updateInPlace`; the new body is `updateViaWorktree`; `Update`
  dispatches on `opts.InPlace`.
- `--help` text documents the default, the worktree location, and the
  leave-on-failure / resume behavior.

### Touch points in `pn` (Go)

- `internal/workspace/update.go` — dispatch + new `updateViaWorktree`; extract
  `updateInPlace` from the current body. Reuse `topoAlpha`, `requireTerminal`,
  rev-lock seeding/writing, eventlog, `isDirty`, `hasUpstream`, `captureHead`.
- New small helper (e.g. `internal/workspace/update_worktree.go`) for the
  per-repo lifecycle (add/integrate/cleanup) and the primary-state probe
  (on-main-clean / on-other-branch / on-main-dirty) via
  `git rev-parse --abbrev-ref HEAD` + the existing dirty check.
- `internal/workspace/worktree.go` — `WorktreeList` must **skip dot-prefixed
  entries** under `worktrees_dir` so the `.pn-update/` directory is never listed
  as a coordinated set. (Coordinated-set branch names are non-dot by convention.)
- All paths derive from `ws.root`; no change to root resolution.

### Supporting libraries / helpers (audit + likely-minimal changes)

- `lib/scripts/update-locks-lib.bash` — **audit, expected no change.** One noted
  interaction: it toggles `core.fsmonitor`, which lives in the _shared_
  `.git/config` (common across worktrees), so the primary's fsmonitor is briefly
  disabled during a repo's run and restored on exit by the lib's trap. Perf-only,
  self-healing; documented as an edge case.
- `modules/ul/determine-ul-lib-dir/determine-ul-lib-dir.sh` — **no change**
  (bypassed by injected `UL_LIB_DIR`).
- Each consumer `update-locks.sh` (6 repos) — **audit** for any `WORKSPACE_ROOT`
  / absolute-sibling-path assumption that breaks when `$SCRIPT_DIR/..` is
  `.worktrees/.pn-update/` instead of the workspace root. **Review 2026-06-24:**
  none of the current six dereference `WORKSPACE_ROOT` beyond passing it to the
  resolver, and the shared lib never reads it at runtime — so the audit risk is
  confined to _future_ scripts that reach for siblings via `WORKSPACE_ROOT`. The
  load-bearing requirement is the non-empty `UL_LIB_DIR` injection (B1).

### Docs / instructions

- `docs/worktrees.md` — add a third model section distinguishing **per-repo
  ephemeral update worktrees** (this design; transient, remote-relock,
  auto-cleaned) from the **coordinated set** and **single-override** models.
- `pn-workspace-rules/CLAUDE.md` + `USER_JOURNEYS.md` — document the new default,
  the resume workflow for a left-behind worktree/branch, and the
  primary-state-at-integration behavior.
- New **ADR** under `docs/adr/` recording the default flip and the
  per-repo-worktree (vs coordinated-set) decision and its rationale.

## Edge cases

- **Primary on another branch / dirty at integration** — handled by step 7
  (ff the ref / defer). The long phase never required the primary to be on
  `main`, so the user is free to work in the primary clone throughout; only a
  dirty _`main`_ checkout defers.
- **Remote `main` advances mid-run** — caught by the step-5 re-fetch+rebase; a
  late advance in the push race window rejects the ff push (step 6) → leave+record
  (see the asymmetric-state note in step 7).
- **Upstream repo failed earlier** — downstream relocks against the un-bumped
  remote upstream (continue policy); surfaced only via per-repo outcomes.
- **`WorktreeList` / scanners** — dot-prefixed `.pn-update/` is skipped by list
  and (already) by `init`/`reconcileFromFilesystem`'s dot-skip.
- **Stale leftovers** — a previously failed run's `W`/`B` exist; step-1
  `worktree add` fails fast (dir/branch exists) and that repo records an error
  naming the path to clean. `pn workspace worktree prune` clears stale admin
  entries.
- **No-op update** (everything current) — `update-locks.sh` makes only stamp
  commits; `B` still fast-forwards `main` (stamp commits), or is an empty ff;
  cleanup proceeds.
- **Dirty-repo handling differs by mode.** `--in-place` is identical to today,
  _including the upfront dirty-repo skip_ (`update.go:107`). The default worktree
  flow **does not** skip a repo whose primary tree is dirty: `git worktree add -b
B W main` succeeds regardless and leaves the primary untouched, so the long run
  proceeds; only a dirty _`main`_ checkout _at integration time_ (step 7) defers.
  The implementer must NOT port the upfront dirty-skip into the worktree path.
- **Interrupted run (SIGINT/SIGTERM).** `update-locks.sh` traps INT/TERM and rolls
  back its worktree contents, but an interrupt _between_ `pn` steps — notably after
  step 6's push, before step 7 — leaves the same asymmetric state as the B2 defer,
  with no summary emitted. Recovery is identical (reset local `main` to the pushed
  remote); the leftover worktree/branch is cleaned with `pn workspace worktree
prune` + `git branch -D`.
- **Second shared-state interaction (besides fsmonitor).** `update-locks.sh`'s
  pre-commit-hook cache marker under `$XDG_STATE_HOME/.../<project>/` is keyed by
  project name and shared between the primary and the worktree. Benign (a drv-path
  marker), but the same shared-state class as the fsmonitor toggle — include it in
  the audit.

## Tests

- **Unit (mock `exec.Runner`)** — per-repo flow issues the expected git sequence;
  the three integration branches (clean-main ff-merge / other-branch ref-ff /
  dirty-main defer); each failure point leaves W+B and records the right
  `failed_step`; success removes W+B and records the rev. `--in-place` dispatches
  to the preserved body.
- **Smoke (real `pn` against bare remotes)** — extend the
  `smoke_bare_remote.go` harness:
  - happy path: update isolates in `.worktrees/.pn-update/<repo>-<ts>`,
    integrates to primary `main`, pushes to the bare remote, removes W+B; primary
    `main` advanced.
  - failure path (e.g. forced push rejection): repo records `failed@push`, W+B
    remain, primary `main` unchanged for that repo, summary names it.
- **Isolation invariant** — analogue of the coordinated-set "P1": for a repo that
  fails before integration, the primary's `HEAD`, branch, index, and working tree
  are byte-identical to pre-run; on success, primary `main` advanced and W+B are
  gone. (Distinct from P1: integration _does_ deliberately advance primary `main`
  — that is the intended end state, only at the end.)
- **`revs.json`** — records the pushed remote rev after integration; a
  pushed-but-deferred repo records the pushed `B`/remote tip (not stale local
  `HEAD`); repos failing before push keep prior entries.
- **Divergence / asymmetric-state smoke (required).** Force a repo to have an
  unpushed local `main` commit _and_ an advanced `origin/main`, run update, and
  assert: `B` is pushed to remote `main`, step 7 defers, W+B remain, the summary
  prints the `reset`/`branch -f` recovery (not a merge), and `revs.json` records
  the pushed remote rev.
- **`ResolveULLibDir` failure path.** With the resolver forced to return `""`,
  assert the worktree flow errors rather than silently relocking (B1).
- **Distinct run stamps.** The per-run stamp is sub-second + PID, so concurrent
  updates get distinct branch/worktree names and never collide at `git worktree
add -b`; a losing run instead has its step-6 push rejected (non-fast-forward)
  and that repo fails — it does not half-create a worktree. (There is no explicit
  collision pre-flight guard; stamp uniqueness is the safety property.)

## Follow-up implementation beads (proposed — to create on approval, via `bd`)

1. **CLI `--in-place` + default flip** — `UpdateOptions.InPlace`, flag on
   `update`/`upgrade`, extract `updateInPlace`, dispatch. _(small)_
2. **`updateViaWorktree`** — the per-repo lifecycle + smart integration + summary;
   the bulk of new Go. _(large)_
3. **`WorktreeList` dot-skip** — skip dot-prefixed entries under `worktrees_dir`.
   _(tiny)_
4. **Unit tests** — flow, integration branches, failure-leaves, success-cleanup.
5. **Smoke tests** — happy + failure scenarios on the bare-remote harness.
6. **Isolation-invariant test** — primary untouched on failure / advanced on
   success.
7. **Consumer `update-locks.sh` audit** — verify/repair each repo's script under
   worktree execution.
8. **Docs + ADR** — `worktrees.md`, agent-rules, `USER_JOURNEYS.md`, `--help`,
   new ADR.

## Refinements resolved during review (2026-06-24)

- **Local-`main` rebase is an explicit step, ordered before the `origin/main`
  rebase** (steps 4 then 5), because local `main` may carry unpushed commits made
  during the long run; those must be incorporated before remote advances
  (Phillip).
- **Publish from the worktree** (`push HEAD:main`) rather than pull-rebase+push on
  the primary's current branch, so the smart-integration "primary on another
  branch" case works. End state identical (remote + local `main` both advance).
- **Leave-on-any-failure, remove-only-on-success** is the adopted rule; the three
  named failure cases (rebase-abort, ff-fail/defer, push-fail) are instances.
- **ADR recorded:** `docs/adr/0009-pn-workspace-update-worktree-isolation.md`
  captures the default-behavior flip and the per-repo-worktree (vs coordinated
  set) decision.

## Open items

None blocking. Remaining confirmation is Phillip's overall approval of this spec
before it goes to `writing-plans`.
