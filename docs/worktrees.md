# Working with worktrees

> **Three models exist.** The **ephemeral-update model** (the `update`/`upgrade` default) creates short-lived per-repo worktrees on throwaway branches for each run of `pn workspace update`, keeping the canonical clones free during the long relock. The **coordinated-set model** (`pn workspace workforest`) creates a full workspace of worktrees — every repo on a shared feature branch — and is the recommended approach for cross-repo feature work. The older **single-override model** (`--override-path` / `PN_WORKSPACE_OVERRIDE_PATHS`) patches one or more repo paths into an existing workspace run; it is still valid for single-repo overrides. All three are documented here.

## Per-repo ephemeral update worktrees (the `update` default)

`pn workspace update` (and the update phase of `upgrade`) isolates each repo's relock work in a
short-lived git worktree on a throwaway branch. The canonical clones and their `main` branches
stay free during the long `update-locks.sh` run; `main` is only touched by a fast fast-forward at
the very end. See [ADR 0009](adr/0009-pn-workspace-update-worktree-isolation.md) for the full
rationale.

### How it works

For each repo in topological order:

1. Create a worktree at `<root>/.workforests/.pn-update/<repo>-<run-ts>` on branch
   `pn-update/<run-ts>`, branched off local `main`.
2. Run `./update-locks.sh` in that worktree (the same script as before — nothing changes about
   what gets locked or how). With `--siblings-only`, relock just the workspace-sibling inputs
   instead and skip the script.
3. Rebase the result onto local `main` (catching any unpushed local commits), then onto
   `origin/main` (catching any remote advances). Local-before-remote order is deliberate.
4. Fast-forward the primary `main` — smart integration based on the primary's state:
   - **Clean `main` checkout** → `git merge --ff-only`
   - **Another branch checked out** (main not checked out) → ref-only fast-forward
     (`git fetch . pn-update/<run-ts>:main`), leaving in-progress work on the other branch
     completely untouched
   - **Dirty `main` checkout** → defer: leave the worktree + branch, report, continue to next
     repo

5. Remove the worktree and branch on success.

**`update` never pushes.** It is local-only: it fetches and rebases, but every commit it makes
stays on the local `main`. Publishing — and the workspace-sibling lock propagation that has to be
interleaved with publishing — belongs to `pn workspace push`. See
[ADR 0023](adr/0023-workspace-push-owns-sibling-propagation.md), which amends ADR 0009 on exactly
this point. Anything that relied on `update` (including `update --siblings-only`) to publish must
call `pn workspace push`.

### Leave-on-failure and resuming a left-behind worktree

Any failed step leaves that repo's worktree and branch in place for inspection. The sweep
continues to the next repo. The end-of-run summary names each repo's outcome, the step it stopped
at, the actual git error, and a recovery hint.

This is `pn workspace update`'s own recovery path for its own worktree-isolated flow: the
worktree and branch inspected/removed below are that flow's ephemeral artifacts, not the
canonical checkout's primary branch. It is not a general recipe for manipulating a repo from the
outside — an agent that finds the canonical unexpectedly off its primary branch or dirty outside
this flow MUST stop and report (Tier R / R-3), not reset, re-checkout, stash, or otherwise work
around it.

To resume or clean up a left-behind worktree:

```bash
# Inspect the worktree — it is a normal git working tree
ls <root>/.workforests/.pn-update/<repo>-<run-ts>
git -C <root>/.workforests/.pn-update/<repo>-<run-ts> log --oneline -5

# If the relock is already done, finish the fast-forward manually:
git -C <root>/<repo> merge --ff-only pn-update/<run-ts>

# To discard and clean up:
git worktree remove --force <root>/.workforests/.pn-update/<repo>-<run-ts>
git -C <root>/<repo> branch -D pn-update/<run-ts>
# Or, to prune all stale update worktrees at once:
pn workspace workforest prune
git -C <root>/<repo> branch -D pn-update/<run-ts>
```

### Integration-defer recovery

This is `pn workspace update`'s own documented recovery step for one specific, bounded failure
mode of its worktree-isolated flow — not a general technique for handling an off-`main`/dirty
canonical elsewhere. Outside this pn-update flow, an agent that finds the canonical unexpectedly
off its primary branch or dirty MUST stop and report (Tier R / R-3) — not reset, re-checkout,
stash, or otherwise work around it.

If the final fast-forward fails (step 4), local `main` is not fast-forwardable to the relocked
branch — typically because `origin/main` advanced mid-run, so the branch (which was rebased onto
`origin/main`) is no longer a descendant of local `main`. The worktree and branch are left in
place and the run summary names the recovery, which targets **the relocked branch**:

```bash
# When main IS checked out in the primary:
git -C <root>/<repo> reset --hard pn-update/<run-ts>

# When main is NOT checked out (on another branch):
git -C <root>/<repo> branch -f main pn-update/<run-ts>
```

Recover to the **branch**, not to `origin/main`. The branch carries this run's relock plus any
unpushed local commits it replayed; resetting to `origin/main` would discard both.

> **ADR 0009's "asymmetric defer state" no longer arises here.** That hazard was remote `main`
> advancing (via update's own push) while local `main` stayed behind. Since `update` no longer
> pushes ([ADR 0023](adr/0023-workspace-push-owns-sibling-propagation.md)), nothing update does can
> put the remote ahead — so a defer can only leave local `main` behind its own worktree branch,
> which the commands above fast-forward.

Run the matching command above only in response to the run summary reporting this specific
state — it is `pn workspace update`'s prescribed recovery for its own integration-defer failure
mode, not a general fix for an off-branch/dirty canonical.

### `--in-place` escape hatch

`pn workspace update --in-place` (and `pn workspace upgrade --in-place`) runs the original
direct-on-`main` flow, including the upfront dirty-repo skip. Use it when:

- You want to debug a relock without the worktree machinery.
- The worktree flow itself is failing and you need to fall back.

The default worktree flow does **not** skip a dirty repo upfront — the worktree isolates the
primary, so the long run proceeds regardless. Only a dirty `main` _checkout_ defers at
integration.

### `--siblings-only` surgical relock

`pn workspace update --siblings-only` relocks ONLY the `phillipgreenii-*` workspace-sibling flake
inputs — the `propagateWorkspaceEdges` pass (`nix flake update --refresh <sibling-alias>`) — and
**skips each repo's `update-locks.sh`**. The result: `nixpkgs` and other third-party inputs are
left untouched (each repo's `flake.lock` diff shows only sibling inputs moved). It is the surgical
way to clear `pn workspace doctor`'s `flake-lock-fresh` findings without a full
`nix flake update`, and is exactly what `doctor --fix` runs for those findings. It composes with
`--in-place`, and — because `update-locks.sh` never runs — it does **not** resolve or require
`UL_LIB_DIR`, so it succeeds headless (no nix resolver).

Like plain `update`, it is **local-only**: each sibling is resolved against that sibling's
**declared remote URL** and the bump is committed locally, with no push. It therefore converges to
whatever is already published — which is exactly what `flake-lock-fresh` compares against (that
check reads the target's remote head via `git ls-remote`), so the fix is complete without touching
a remote.

What it CANNOT do is converge onto a sibling rev that exists only locally — a lock can only pin a
published rev. Cross-repo convergence after a local land is therefore `pn workspace push`'s job
(ADR 0023). It also converges only branch-tracking inputs: a sibling pinned to an explicit
`?rev=`/non-default branch will not move.

Note the difference from plain `update`: plain `update` has **no** sibling pass at all. A repo that
happens to be checked out in the workspace is treated exactly like one that is not —
`update-locks.sh`'s `nix flake update` relocks every input, sibling or otherwise, from its declared
remote. `--siblings-only` is the caller explicitly asking for that narrow subset, not a special
case `update` applies on its own.

### `update` inside a coordinated workforest set requires `--in-place`

Running bare `pn workspace update` from inside a coordinated set (created by
`pn workspace workforest add`) is an error. The worktree-isolation flow only runs from the canonical
workspace root. Inside a set, use:

```bash
pn workspace update --in-place
```

This relocks the set's worktrees in place, which is the correct behavior for a set: the set's
per-repo working trees are already isolated, so the worktree-isolation machinery is redundant and
would conflict with the set's own invariants.

### Concurrent runs

Running two `pn workspace update` invocations simultaneously in the same workspace is **not
coordinated**. Each gets a **distinct** branch name (the `pn-update/<run-ts>` stamp is a sub-second
timestamp + PID), so they do not collide at `git worktree add`; but both fast-forward the same local
`main`, so the second run to reach a given repo's integration finds `main` already advanced, is not
fast-forwardable, and defers that repo. Run updates serially.

---

## Coordinated workforest sets (recommended for cross-repo work)

`pn workspace workforest add <branch>` creates a **coordinated set**: a directory that is itself a complete workspace root, containing a git worktree for each member repo, all on the shared `<branch>`. By default a set contains **every** repo in `pn-workspace.toml`; pass `--repos` to create a **subset** set (see [Subsetting a set](#subsetting-a-set-repos-add-repo-remove-repo)). The normal `pn workspace` verbs (`build`, `update`, `rebase`, `push`, `status`, etc.) run unchanged inside the set because the set directory satisfies the existing path model — `pn` finds the set's own `pn-workspace.toml` via the upward walk and resolves all repo paths as `{set}/{repo}`.

### Quick start

```bash
# From the canonical workspace root:
pn workspace workforest add my-feature        # create set at .workforests/my-feature
cd .workforests/my-feature
unset PN_WORKSPACE_ROOT                       # must not point at the canonical root
pn workspace status                           # operates on the set's worktrees
pn workspace build
pn workspace rebase main                      # rebase each worktree onto local main
pn workspace push --set-upstream              # publish the feature branch (first time)
```

### Workforest set commands

| Command                                                                 | What it does                                                                                                                                                              |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pn workspace workforest add <branch> [<commit-ish>] [--repos]`         | Create a set for `<branch>` under `workforests_dir` (default `.workforests`). All repos by default, or a subset with `--repos a,b`. Mirrors `git worktree add` semantics. |
| `pn workspace workforest add-repo <branch> <repo>`                      | Add one repo to an existing set without recreating it. Mirrors `git worktree add`.                                                                                        |
| `pn workspace workforest remove-repo <branch> <repo>` (alias `rm-repo`) | Remove one repo from an existing set; mirrors `git worktree remove` (refuses dirty/locked unless `--force`). Does NOT delete the branch; refuses to remove the last repo. |
| `pn workspace workforest list`                                          | List existing sets.                                                                                                                                                       |
| `pn workspace workforest remove <branch>` (alias `rm`)                  | Remove a whole set; mirrors `git worktree remove` (refuses dirty/locked unless `--force`). Does NOT delete the branch.                                                    |
| `pn workspace workforest prune`                                         | Run `git worktree prune` in every canonical repo (clears stale admin entries after a manual `rm -rf` of a set).                                                           |

The `workforests_dir` field in `pn-workspace.toml`'s `[workspace]` table configures where sets live (default: `.workforests`, relative to workspace root). When it uses a non-dot-prefixed value, the filesystem scanners skip it automatically.

### Subsetting a set (`--repos`, `add-repo`, `remove-repo`)

A set may contain a **subset** of the workspace repos instead of all of them:

```bash
# Create a set with only two repos:
pn workspace workforest add my-feature --repos phillipg-nix-repo-base,phillipgreenii-nix-support-apps

# Grow / shrink an existing set without recreating it:
pn workspace workforest add-repo    my-feature phillipgreenii-nix-overlay
pn workspace workforest remove-repo my-feature phillipgreenii-nix-overlay   # alias: rm-repo; --force for dirty/locked
```

How membership is tracked and how dependencies resolve:

- **Per-set membership lives in the set's own `pn-workspace.toml`.** A subset set's copied config/lock/revs are filtered to the member repos; the canonical `pn-workspace.toml` is never modified. `add-repo`/`remove-repo` rewrite the set's filtered config/lock/revs from canonical, so the set always stays a valid, self-contained workspace.
- **Excluded workspace dependencies resolve against their locked flake input.** If a member repo declares a workspace dependency on a repo that is _not_ in the set, the set's lock drops that override edge — so nix resolves the input from the consumer's own locked flake input (the published/canonical source) rather than a set-internal `git+file://` override. `workforest add`/`add-repo`/`remove-repo` print a notice naming each such `consumer → dependency` edge so the fallback is never silent.
- **`remove-repo` refuses to remove the last repo** (use `workforest remove <branch>` to delete the whole set) and, like `git worktree remove`, leaves the branch behind in the canonical repo.
- **P1 still holds.** Subset `add`/`add-repo`/`remove-repo` only `git worktree add`/`remove` against canonical repos (shared object store + admin entries) and write inside the set; no canonical working tree, index, HEAD, or branch is modified.

### Key caveats

- **`PN_WORKSPACE_ROOT` must be unset (or point at the set).** If it is exported pointing at the canonical root, `pn` resolves the primary workspace instead of the set. Unset it before running any verb inside a set.
- **P1 guarantee:** no `pn workspace` verb run from inside a set modifies the canonical checkouts' working state (HEAD, branch, index, working tree). New commits/branches/objects land in the shared object store. `update`/`rebase` may update shared remote-tracking refs and `FETCH_HEAD` but never the primary's working tree or checked-out branch.
- **Hooks must use workspace-relative paths.** Hooks that hard-code absolute canonical paths escape the set and violate P1. Use `{root}/{repo}` construction (where `root` is resolved from cwd or `PN_WORKSPACE_ROOT`) rather than hard-coded absolute paths.

For the full agent-rules treatment of coordinated workforests — including the `rebase [branch]` and `push --set-upstream` forms, the `WORKSPACE_ROOT` recompute-from-`SCRIPT_DIR` note, and the detailed P1 explanation — see [`pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`](../pn-workspace-rules/skills/pn-workspace-rules/SKILL.md), section **Coordinated Workforest Sets**.

---

## Single-repo override model (`--override-path`)

Agents commonly create git worktrees of workspace projects, either outside the workspace directory or under `<repo>/.git/worktrees/<name>`. To make `pn-workspace-*` commands operate on a worktree path instead of the original sibling directory, use `--override-path` (or the `PN_WORKSPACE_OVERRIDE_PATHS` env var).

The override key is the workspace **directory name** (the basename of the project's path in `pn-workspace.lock`). It works for terminal and non-terminal projects identically.

## Single override

CLI:

```bash
pn-workspace-build --override-path phillipg-nix-repo-base=$HOME/worktrees/repo-base-feature-foo
```

Env:

```bash
export PN_WORKSPACE_OVERRIDE_PATHS=phillipg-nix-repo-base=$HOME/worktrees/repo-base-feature-foo
pn-workspace-build
```

## Multiple overrides

CLI: repeat the flag.

```bash
pn-workspace-build \
  --override-path my-other-repo=$HOME/worktrees/my-other-repo-feature-foo \
  --override-path phillipgreenii-nix-personal=$HOME/worktrees/nix-personal-feature-foo
```

Env: comma-separated.

```bash
export PN_WORKSPACE_OVERRIDE_PATHS=my-other-repo=/path/p,phillipgreenii-nix-personal=/path/c
```

Flags win over env per key. Validation rejects unknown project names, missing directories, and paths without `flake.nix`.

## Workspace root

When running from inside a worktree, `pn-workspace-*` cannot walk up to find the workspace root. Pass `--root` (preferred) or set `PN_WORKSPACE_ROOT`:

```bash
pn-workspace-build --root ~/workspace --override-path repo-base=$PWD
```

```bash
export PN_WORKSPACE_ROOT=~/workspace
export PN_WORKSPACE_OVERRIDE_PATHS=repo-base=$PWD
pn-workspace-build
```

The deprecated `--workspace` flag continues to work as an alias for `--root` and emits a deprecation notice on stderr.

## Caveats

- Status, update, push, and rebase operate on the swapped path. `git push` pushes the worktree's branch, `git mu` (rebase) rebases it, etc.
- Overrides are transient. They never modify `pn-workspace.lock`.
- Stale env entries pointing at deleted worktrees are caught at validation before nix runs.
