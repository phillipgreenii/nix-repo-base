# Handoff prompt — implement coordinated git worktrees for `pn workspace` (pg2-i5lf)

_Paste the block below to start the next session. It drives subagents through every
implementation bead, in isolated git worktrees, with a final review at the end._

---

You are implementing the **coordinated git worktrees** feature for the `pn`
workspace CLI. The design is finished and committed; your job is to build it bead
by bead, in isolated git worktrees, then run a final review.

## Read first (before touching anything)

1. Run `bd prime` for workflow context and the session-close protocol.
2. Read the design — it is the **source of truth**:
   `phillipg-nix-repo-base/docs/superpowers/specs/2026-06-16-pn-workspace-coordinated-worktrees-design.md`
   (committed at `b778634`). Pay special attention to **P1** (the primary
   checkouts are never modified — enforced structurally) and the **no
   command-specific logic** principle.
3. `bd show <id>` each bead below for its acceptance details.

All code lives in `phillipg-nix-repo-base/modules/pn` (Go). Note the meta-irony:
you are building cross-repo worktrees while working in a single-repo worktree —
that's fine, the feature doesn't exist yet, so use ordinary `git worktree`.

## Beads and execution order (respect the dependency DAG)

```
pg2-2cew  worktrees_dir config field            (foundation; do first)
   └─► pg2-kf6h  worktree add/list/remove/prune  (the bulk of the feature)
          ├─► pg2-uy83  P1 invariant test        (gates the feature)
          ├─► pg2-7tqh  worktree smoke tests
          ├─► pg2-swym  verify build/update/status in a set
          └─► pg2-4kto  agent-facing docs        (do last)
pg2-rnjz  rebase [branch] + push --set-upstream  (independent; also gates uy83/7tqh)
pg2-ihx4  apply cache-key fix                     (independent)
```

- The **spine** `pg2-2cew → pg2-kf6h → {pg2-uy83, pg2-7tqh, pg2-swym} → pg2-4kto`
  is sequential.
- `pg2-rnjz` and `pg2-ihx4` are independent of the spine and may be done
  concurrently in their own worktrees, then merged into the feature branch —
  **but merge `pg2-rnjz` before `pg2-uy83`/`pg2-7tqh`**, since the P1 test and
  smoke tests exercise `rebase`/`push`.
- **Do NOT implement `pg2-dirg`** (repo subsetting) — it is deliberately deferred.

## Working method

- Use the **`using-git-worktrees`** skill to create a feature worktree off `main`
  for `phillipg-nix-repo-base`, branch **`pn-coordinated-worktrees`** (a simple
  name — this is a personal nix repo, not the ZR monorepo, so no
  `phillipg.TICKET` prefix and no `Refs:` commit line). Do **all** work in the
  worktree; never modify the primary checkout's working tree.
- Drive with **`subagent-driven-development`**: one subagent per bead, dispatched
  in dependency order. When you run independent beads (`pg2-rnjz`, `pg2-ihx4`)
  concurrently, give each its own worktree (`dispatching-parallel-agents` +
  `using-git-worktrees`) so they don't collide, then merge into the feature
  branch.
- **TDD** (`test-driven-development` skill): for each bead, write the failing test
  straight from its acceptance criteria first, then implement. The design's
  **Tests** section enumerates exactly what to cover.
- Per-bead loop: `bd update <id> --claim` → implement (TDD) → run the gates below
  → commit (conventional message, e.g. `feat(pn): …` / `test(pn): …`; **never use
  `run_in_background` for git commits**) → `bd close <id>`.
- Match surrounding Go style. Keep the path model unchanged — P1 holds because
  every path stays `{ws.root}/{repo}`; don't add a resolver or a `canonical_root`
  fallback (the design explains why those were rejected).

## Verification gates (per bead, and again before the final review)

- `go test ./...` green in `modules/pn`.
- Smoke tests where relevant: `go test -tags smoke ./internal/workspace/smoke/...`
  (they build the real `pn` binary against real-git temp workspaces).
- `nix flake check` green for `phillipg-nix-repo-base`.
- `pn` builds and the new/changed verbs behave per the design.

## Final review (the explicit deliverable)

After every bead is closed and the gates pass:

1. Dispatch a **code-review** subagent (or run the `code-review` skill) over the
   full feature-branch diff vs `main`. It must check the change against the
   design's acceptance criteria, and adversarially verify the two load-bearing
   invariants: **P1** (no canonical-checkout mutation — confirm the P1 test
   actually exercises every verb and passes) and **no worktree-conditional
   logic** (rebase keys on its arg, push on its flag, the apply fix is a general
   re-key).
2. Address must-fix findings.
3. Re-run the full gate suite (`verification-before-completion` skill) — evidence
   before claims.
4. Summarize for Phillip: what landed, test/gate output, the P1 test result, and
   any new follow-ups filed in `bd`. **Leave the branch ready for review — do not
   merge to `main` or push to a remote without Phillip's go-ahead.** Close
   `pg2-i5lf` only after the work is merged or Phillip says so.

## Guardrails

- The design doc is authoritative. If code reality contradicts it, **surface the
  conflict** — don't silently diverge.
- Mirror `git worktree add/remove/prune` semantics exactly (the design requires
  it): `add` uses git's start-commit default (current `HEAD`); `remove` does not
  delete the branch; `prune` mirrors `git worktree prune`.
- `update` must **not** auto-set-upstream; publishing a fresh branch is the
  explicit `pn workspace push --set-upstream`.
- No-op on no remote branch is intended behavior for `rebase`/`push`/`update`,
  not a bug to "fix."
- Don't push or merge to `main` without Phillip's approval.
