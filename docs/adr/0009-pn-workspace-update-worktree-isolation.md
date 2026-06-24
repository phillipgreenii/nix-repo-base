# `pn workspace update` isolates per-repo work in ephemeral git worktrees

**Status**: Proposed
**Date**: 2026-06-24
**Deciders**: Phillip Green II

## Context

`pn workspace update` runs, per repo in topological order, directly on the
primary checkout's `main`: `git pull --rebase --autostash` → `./update-locks.sh`
(`nix flake update`, package-hash bumps, `nix fmt`, per-step commits) →
`git push` (`internal/workspace/update.go`). It relocks each repo against the
**remote** commit of its upstream siblings — consumer flakes pin
`github:phillipgreenii/<repo>` and the lock records the remote rev (verified:
`nix-personal/flake.lock` pins repo-base `a5b0cbf8…`, identical to
`pn-workspace.revs.json`). Topological order plus push-before-downstream is
load-bearing: an upstream must be pushed before a downstream's `nix flake update`
can pick up its new remote commit.

The sweep is slow (nix evaluation, network fetches, hashing, formatting, commits
across ~6 repos) and, while it runs, owns every primary checkout — each repo's
`main`, index, and working tree are mutated in place — so the canonical clones
are unusable for other work until it finishes. Phillip wants the long, churny
part kept **off** the primary checkouts, touching the primary `main` only with a
fast fast-forward at the very end, so the clones stay free for parallel work.

A coordinated worktree _set_ already exists (the
`docs/superpowers/specs/2026-06-16-pn-workspace-coordinated-worktrees-design.md`
design spec): one directory that is a complete workspace whose repos are
worktrees on a shared branch, relocked via local `--override-input git+file://`
paths. That model is **wrong for update**:
it would lock each repo against its local sibling worktree path, not the remote
commit the lock must record. Update's correctness depends on relocking against
pushed remote revs.

## Decision

**Make `pn workspace update` isolate each repo's work in an independent,
ephemeral git worktree on a throwaway branch, integrated back onto the primary
`main` by fast-forward; this becomes the default, with `--in-place` restoring the
direct-on-`main` flow.**

1. **Per-repo ephemeral worktrees, not a coordinated set.** For each repo in
   topological order: create a worktree + branch off local `main` under
   `{root}/.worktrees/.pn-update/<repo>-<run-ts>` (branch `pn-update/<run-ts>`),
   run the existing `./update-locks.sh` there, integrate back, and remove the
   worktree + branch on success. The coordinated-set model is explicitly **not**
   reused, because its local `git+file://` relock would record the wrong (local,
   not remote) sibling revs.

2. **Relock correctness is unchanged.** Each repo still relocks against **remote**
   siblings; topological order and push-before-downstream are preserved. The win
   is isolation, not a change to what gets locked.

3. **Integration is a fast-forward, smart about the primary's state.** After
   relocking, the branch is rebased onto local `main` (catching unpushed local
   commits) then `origin/main` (catching remote advances) — the local-before-remote
   order is deliberate — pushed to remote `main` from the worktree, and the local
   primary `main` is advanced:
   `merge --ff-only` when on a clean `main`; a ref-only fast-forward
   (`fetch . <branch>:main`) when `main` is not checked out (so in-progress work
   on another branch is undisturbed); **defer** (leave the worktree + branch,
   report) when `main` is checked out and dirty.

4. **Default flips; `--in-place` is the escape hatch.** `pn workspace update`
   (and `upgrade`'s update phase) use the worktree flow by default. `--in-place`
   runs today's direct-on-`main` body, preserved unchanged. `upgrade` then runs
   `apply` against the primary `main` exactly as before, since integration leaves
   the new locks on the primary `main` in place.

5. **Resilient, foreground, leave-on-failure.** The run is foreground only
   (isolation alone frees the clones; no background/daemon machinery). Any failed
   step leaves that repo's worktree + branch for inspection / manual resume and
   the sweep continues to the next repo (aggregating failures, as today); the
   worktree + branch are removed only after a fully successful integration. The
   end-of-run summary names each repo's outcome and the step any failure stopped
   at.

The full algorithm, edge cases, and test plan are in
`docs/superpowers/specs/2026-06-24-pn-workspace-update-worktree-isolation-design.md`.

## Consequences

### Positive

- The canonical clones and their `main` branches stay free during the long
  relock/build phase; the primary `main` is touched only by a fast fast-forward
  at the end. Parallel work in the primary clones is unblocked.
- Update correctness (remote relock, topo order, push-before-downstream) is
  preserved — `update-locks.sh` and its contract run unchanged, just in a
  worktree directory.
- Failures are recoverable: a left-behind worktree + branch is a ready-made
  resume point, and partial progress (earlier repos) is already integrated and
  pushed.
- The smart integration keeps `main` advancing even when the primary is checked
  out on another branch, so isolation does not force the user off `main`-adjacent
  work.

### Negative

- New machinery and surface area in `pn`: a per-repo worktree lifecycle, the
  smart-integration state probe, a `--in-place` flag, and a `WorktreeList`
  dot-skip so `.pn-update/` is not mistaken for a coordinated set.
- More git operations per repo (worktree add, two rebases, push, ff, cleanup)
  than the old three-step in-place flow — more places to fail, offset by
  leave-on-failure recoverability.
- A failed/aborted run can leave residue (worktree + branch) that the user (or
  `pn workspace worktree prune`) must clean up.
- **Asymmetric defer state.** If local `main` had unpushed commits _and_
  `origin/main` advanced during the run, the push (step 6) advances remote `main`
  before the step-7 ff fails — leaving **remote `main` advanced and authoritative
  while local `main` is behind, holding orphaned duplicate-SHA commits**. The same
  state can arise from a crash between push and ff. Recovery is to _reset_ local
  `main` to the pushed remote (`git branch -f main origin/main`, or
  `git reset --hard origin/main` when on `main`), **not** a merge — the run summary
  prints this.
- **Requires non-empty injected `UL_LIB_DIR`.** Each worktree clobbers
  `WORKSPACE_ROOT` to `SCRIPT_DIR/..`, so the only safe relock path is `pn`
  injecting a resolved `UL_LIB_DIR`; the worktree flow hard-errors on an empty
  `ResolveULLibDir` result rather than silently taking the store fallback. The
  in-place flow has no such requirement.
- **Concurrent runs unsupported.** All repos in one invocation share the branch
  name `pn-update/<run-ts>`; a second concurrent `update` in the same workspace
  collides on the branch/worktree and must fail fast.
- `update-locks.sh` toggles `core.fsmonitor`, which lives in the shared
  `.git/config`; during a repo's run the primary's fsmonitor is briefly disabled
  and restored on exit — perf-only, self-healing, but a shared-state interaction
  worth noting.

### Neutral

- **Dirty-repo handling differs by mode.** `--in-place` retains the exact prior
  behavior, including the upfront dirty-repo skip, for anyone who wants the old
  flow or to debug. The default worktree flow does **not** skip a dirty repo — the
  worktree isolates the primary, so the long run proceeds regardless; only a dirty
  `main` _checkout_ defers at integration.
- The coordinated worktree _set_ model is unaffected and remains the tool for
  cross-repo feature work; this decision concerns `update` only.
- Worktrees live inside the workspace root (`.worktrees/.pn-update/`), so the
  canonical _working trees_ are untouched but the disk churn is still under the
  workspace root. Each worktree is a full working tree (the object store is shared
  via a `.git` pointer file — checkout cost, not clone cost); on a clean run only
  one is live at a time, but a fully-failed sweep can leave up to N (~6) worktrees
  and their branches behind under leave-on-failure, cleaned via
  `pn workspace worktree prune` then `git branch -D`.

## Alternatives Considered

### Reuse the coordinated worktree set (`pn workspace worktree`)

Rejected: its local `git+file://` relock records local sibling paths, not the
remote revs the lock must pin. Correct for cross-repo _feature_ work, wrong for
_update_.

### Keep update in-place; rely on the user to start it and walk away

Rejected: this is the status quo and is exactly the pain — the clones are
unusable for the duration. Isolation is the requested outcome.

### Background/detached execution instead of (or in addition to) worktrees

Deferred/declined: isolating in worktrees already frees the clones for parallel
work in another terminal/session. Detached execution would add process
management, log streaming, and a status/attach command for a smaller marginal
gain; not pursued now.

### Opt-in `--worktree` flag (default stays in-place)

Rejected in favor of flipping the default: the worktree flow reaches the same end
state (updated, pushed `main`) _plus_ isolation, so it is the better default;
`--in-place` covers the rare case.

## Related Decisions

- Builds on ADR [0002](0002-pn-workspace-toml-schema.md) (`pn-workspace.toml`
  schema) and the `2026-06-16-pn-workspace-coordinated-worktrees` design (whose
  _set_ model is deliberately not reused here).
- Implementation tracked in
  `docs/superpowers/specs/2026-06-24-pn-workspace-update-worktree-isolation-design.md`
  and its follow-up beads.
