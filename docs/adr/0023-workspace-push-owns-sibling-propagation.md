# ADR-0023: `pn workspace push` owns sibling propagation; `update` never pushes

**Date:** 2026-07-29
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

`pn workspace update` pushes to `origin` unconditionally for every repo in its worktree-isolated
flow (`internal/workspace/update_worktree.go`). That was never an intended responsibility of
`update` — it was introduced earlier and carried over unnoticed when `--siblings-only` was added.
It surprises a caller who runs `pn workspace update --siblings-only` expecting a local-only relock
and instead gets a push to `origin/main` (bead `pg2-j2f8f`).

The push also happens **from the throwaway worktree** at
`.workforests/.pn-update/<repo>-<ts>`, which fails two ways (bead `pg2-x42j3`):

1. The generated `.pre-commit-config.yaml` is a per-machine nix-store symlink that is untracked and
   gitignored. `git worktree add` checks out only **tracked** files, so the config is absent in the
   temp worktree and the `prek` pre-push hook aborts with `config file not found`. From the
   canonical clone the symlink exists and the hook passes.
2. `git worktree add` hung ~21 minutes at 0% CPU on a stuck `git fsmonitor` daemon in
   fsmonitor-enabled repos (bead `pg2-pi5u1`).

Observed directly: `pn workspace update --siblings-only` repeatedly failed `repo-base` at the push
and left `.pn-update` worktrees behind; `pn workspace push` — which pushes from the **canonical
clones**, with no worktree — succeeded; the relock only completed via `--in-place`.

The push cannot simply be deferred to the end of the run. Sibling propagation relocks each consumer
against its upstream's **remote** tip via `nix flake update --refresh <aliases>`
(`internal/workspace/propagate.go`, invariant C1), so a consumer can only relock to a rev that is
**already pushed**. Push and propagation are therefore interleaved by nature: push A, relock A's
consumers, push them, and so on in topological order. Any design that separates them has to move
them **together**.

## Decision

**`pn workspace update` MUST NOT push, and MUST NOT do sibling propagation. The interleaved
push-then-propagate loop moves to the existing `pn workspace push` subcommand.**

1. **`update` stops special-casing workspace siblings.** It MUST treat a repo that happens to be
   checked out in the workspace **identically to one that is not**: relock against whatever is on
   the remote. There is no local-sibling path, no `--override-input` to a local checkout, and no
   push-before-downstream ordering inside `update`. Plain `pn workspace update` and
   `pn workspace update --siblings-only` become **local-only**.

2. **`pn workspace push` hosts the whole loop.** For each repo in topological order it pushes, then
   relocks that repo's consumers against its new remote tip, commits, and continues. This is the
   right home because **`push` is already doing a push** — the propagation exists only to serve it,
   and C1's push-before-consumer-relock ordering is intrinsic to pushing, not to relocking.

3. **`push` SHOULD gain an opt-out flag that pushes without the sibling update**, for the case where
   a plain publish is wanted and no lock propagation is intended.

4. **Pushes run from the canonical clone.** `pn workspace push` already does this and it is why it
   works today: the canonical clone has the generated `.pre-commit-config.yaml`, so the `prek`
   pre-push hook finds its config, and no fresh `git worktree add` is performed, so the fsmonitor
   stall cannot occur.

### Relationship to ADR 0009

This **amends** [ADR 0009](0009-pn-workspace-update-worktree-isolation.md) (`Proposed`); it does not
reverse it.

- **Reinforced.** ADR 0009's Context argues at length that update must relock against **pushed
  remote revs**, and rejects the coordinated-set model precisely because its local `git+file://`
  relock "records local sibling paths, not the remote revs the lock must pin". Item 1 above is that
  same principle stated as a uniform rule: local-ness of a repo is irrelevant, the remote is the
  only source.
- **Amended.** ADR 0009's Decision item 3 has the branch "pushed to remote `main` from the worktree"
  as a step of update's integration, and its item 2 assigns "topological order and
  push-before-downstream" to update. Both move to `pn workspace push`. Update's worktree isolation,
  its rebase-then-fast-forward integration onto the local primary `main`, and `--in-place` are all
  **unchanged** — the long, churny relock still happens off the canonical clones, which is what
  ADR 0009 exists to achieve.
- ADR 0009's "Asymmetric defer state" negative consequence — remote `main` advanced while local
  `main` is behind, because the push (step 6) precedes the ff (step 7) — is **eliminated for
  `update`**, since update no longer pushes at all. The same hazard now belongs to
  `pn workspace push`, which pushes from a clean canonical `main` rather than mid-integration, so
  the window is narrower.

## Consequences

- `pn workspace update --siblings-only` becomes local-only, which is what its name implies. Anything
  that relied on it publishing must call `pn workspace push`.
- **`/pn-workspace-sync` and `/pn-workspace-update` must be rewired**, because the sync flow
  currently depends on `update --siblings-only` performing the push. This is a change to a live,
  in-use flow and is the main risk in the change — the rewire must land in the same change as the
  push removal, not after it.
- Both `pg2-x42j3` failure modes are resolved as a side effect rather than being fixed directly:
  pushing from the canonical clone means the generated pre-commit config is present, and no
  `git worktree add` is on the push path.
- Sibling propagation moves out of `update`, so `propagateWorkspaceEdges` and its C1/C2 invariants
  are called from `push`. The invariants themselves do not change; `--refresh` remains mandatory,
  and the clean-tree guarantee (C2) still applies.
- Bead `pg2-j2f8f`'s acceptance criteria are satisfiable as written (plain `update` and
  `--siblings-only` do not push; a separate step performs the push; propagation still converges),
  and `pg2-x42j3` is satisfied by item 4.
- The opt-out flag in item 3 is stated as SHOULD, not MUST — the exact flag name and whether it is
  needed on the first landing are left to the implementation.

## Implementation notes

Recorded when this ADR was implemented (beads `pg2-x42j3` / `pg2-j2f8f`), for the two points the
Decision deliberately left to the implementation.

**The opt-out flag is `pn workspace push --no-siblings`** (item 3), landed with the first
implementation rather than deferred. It is off by default, so a bare `pn workspace push` propagates.
The name mirrors the existing `update --siblings-only` vocabulary — "siblings" is already this
workspace's word for the `phillipgreenii-*` workspace-input set — so the pair reads as opposites.
It is needed on the first landing because propagation makes `push` evaluate nix per repo with
sibling inputs, and `push` was previously a pure git command; `--no-siblings` restores that.

Two behaviours the Decision implies but does not spell out:

- **`push` REFUSES to relock a repo with uncommitted changes**, naming the repo and `--no-siblings`,
  and does not push that repo. The relock ends in a `git commit`, and unlike update's throwaway
  worktree the canonical clone is where a person keeps work — committing a lock bump on top of
  staged changes would sweep them into a `chore(deps): bump` commit. A loud refusal is chosen over a
  silent skip because a silent skip publishes while quietly leaving the locks unconverged, which is
  the failure mode this ADR exists to prevent.
- **Inside a coordinated workforest set, `push` skips the relock** (announced on stderr) and only
  publishes the set's branches. Propagation is a canonical-clone operation (item 4); a set validates
  its siblings through `--override-input` and a subset set has its excluded edges dropped from the
  lock, so relocking there would commit remote-resolved bumps onto the shared feature branch.

**`update --siblings-only` is retained, as a local-only relock.** Item 1 removes update's sibling
SPECIAL-CASING, and that is what plain `update` now has none of: `update-locks.sh`'s
`nix flake update` relocks every input — sibling or not — from its declared remote, so a repo that
happens to be checked out in the workspace is treated identically to one that is not. Under
`--siblings-only` the caller explicitly asks for that narrow subset (sibling aliases only, skip
`update-locks.sh`), and it stays local-only: `nix flake update --refresh <alias>` resolves against
the sibling's declared REMOTE url and the bump is committed, never pushed — exactly the
"local-only" this ADR's Decision item 1 and Consequences ascribe to it.

Retaining it is load-bearing, not conservatism: `pn workspace doctor`'s `flake-lock-fresh` check
compares each consumer's locked sibling rev against `refRev[target]`, which in primary mode is the
target's remote default-branch head (`git ls-remote`, `resolveRefRevs`). The rev to converge on is
therefore already published, so a local relock clears the finding with no push — and
`doctor --fix` delegates to exactly this. Had `--siblings-only` become a no-op, that fix would have
silently stopped fixing. A side effect worth noting: since update no longer pushes, **no**
`doctor --fix` writes to a remote — `attachFlakeLockFix` was previously the sole exception. What it
leaves behind is one unpushed bump commit, which the `branch-synced` check reports as ordinary
landing debt.

What `--siblings-only` cannot do is converge onto a sibling rev that exists only locally; C1 forbids
it. Cross-repo convergence after a local land is `pn workspace push`'s job.

**`update`'s integration-defer recovery hint changed.** With no push, ADR 0009's asymmetric-defer
state cannot arise, so a failed final fast-forward means local `main` is not fast-forwardable to the
RELOCKED BRANCH (typically `origin/main` advanced mid-run). The summary now points recovery at that
branch (`reset --hard pn-update/<ts>` / `branch -f main pn-update/<ts>`), not at `origin/main`:
the branch carries this run's relock plus any local commits it replayed, so resetting to
`origin/main` would discard both.
