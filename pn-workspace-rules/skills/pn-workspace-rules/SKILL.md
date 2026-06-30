---
name: pn-workspace-rules
description: >-
  Use when working in a repo that participates in a `pn-workspace.toml` /
  phillipgreenii nix-* workspace — ANY repo whose flake is declared as a project
  in the workspace. Fires on: running `pn workspace build`, `pn workspace apply`,
  `pn workspace flake-check`, `pn workspace update`/`upgrade`, `pn workspace
  init`/`clone`/`lock`, `pn workspace rebase`/`push`/`status`/`format`/`tree`, or
  any other `pn workspace` verb; deciding whether/when to push a nix-* branch;
  editing `flake.nix` inputs or flake locks across the nix repos; coordinated git
  worktrees / coordinated workforest sets spanning the nix repos; cross-repo
  flake-input changes where one workspace repo's output is consumed by another;
  and the completion gate of "is my task in a pn-workspace repo actually done."
  Also fires when you see `pn-workspace.toml`, `pn-workspace.lock.json`,
  `PN_WORKSPACE_ROOT`, `--terminal`, or `--override-input` and need the workspace
  conventions. Do NOT use for generic nix work unrelated to the `pn` workspace
  tooling.
---

# pn-workspace Conventions for Agents

Rules for AI agents working inside a `pn-workspace.toml` workspace. These apply to ANY repo whose flake is declared as a project in the workspace.

For concrete end-to-end user journeys (the commands a user runs, expected success/error outcomes, and the smoke scenario that exercises each), see `USER_JOURNEYS.md` in this skill directory.

## Cardinal Rule

**Never modify `flake.nix` to point input URLs at local paths.** `pn workspace build/apply/flake-check` inject `--override-input <alias> git+file://<local-path>` at build/eval time — the lock file and flake.nix stay clean. Local-path URLs in `flake.nix` break every other consumer (CI, teammates, future you on another machine).

## Expected (Acceptable) Warnings

`pn workspace build` and `pn workspace apply` print Nix's `warning: not writing modified lock file` on the **success path**. This is **benign and expected** — do not treat it as an error and do not re-investigate it.

**Why it appears:** the warning comes from Nix, not from `pn`. Because every build/apply injects `--override-input <alias> git+file://<local-path>` for each workspace sibling (see Cardinal Rule above), the lock Nix evaluates differs from the committed `flake.lock`. Nix detects the divergence and, since flake inputs were overridden on the command line, intentionally does **not** persist the modified lock — so it warns instead.

**One bullet per overridden input.** Nix emits a separate `not writing modified lock file` line for each `--override-input`. An input STILL appears even when its local rev matches what `flake.lock` records: the redirect from a `github:` (or other) URL to `git+file://` is itself counted as a change, and the override emitter performs no rev-equality check (`helpers.go:55-91` adds one override per consumer edge unconditionally).

**Why it MUST NOT be suppressed in code:**

- No Nix flag silences just this warning. `--no-warn-dirty` is unrelated (it targets dirty-git-tree warnings, not lock divergence).
- `pn` streams Nix's stderr verbatim — it does not parse or filter it (`build.go:56`, `apply.go:81` pass the live sink straight through to `exec.go:79-81`'s `io.MultiWriter`, which copies stderr unmodified).
- Filtering this one line would be brittle and could mask a **real** lock-divergence warning that signals an actual problem.

**Policy:**

- You MUST treat `not writing modified lock file` as a normal, expected line on a successful `pn workspace build`/`apply`.
- You MUST NOT "fix" it by writing local paths into `flake.lock` (the lock equivalent of the Cardinal Rule violation). Doing so corrupts the lock for every other consumer.
- You SHOULD NOT add stderr filtering to `pn` to hide it, for the reasons above.

## Completion Gate

After completing a task in a `pn-workspace.toml` project you MUST validate it before declaring it complete — but match the validation to the change's **blast radius**. `pn workspace build` assembles the entire host system at the shared workspace root; it is the heaviest target and the one every session contends on, so running it unconditionally serializes concurrent task-completions (slow, and the reason multiple wrap-ups hang on each other). Run the lowest tier that actually exercises what you changed.

Pick the tier with this checklist, top to bottom — first match wins:

1. **Did the change touch the assembled system** — `darwinConfigurations` / darwin or home-manager modules, or system module wiring (wherever the module is defined, including a consumed repo) — **or are you about to `apply`/`upgrade`?** → **Tier 3**. Only a full build realizes the host system; `flake check` evaluates the config (and builds its `checks`) but does not realize the system derivation, so a change that evaluates clean can still fail to build.
2. **Does every repo you edited have no consumer** — it never appears as an indented child in `pn workspace tree` (equivalently, it is not a `target` in any `pn-workspace.lock.json` edge)? In this workspace only the terminal `phillipg-nix-ziprecruiter` qualifies. → **Tier 1**.
3. **Otherwise** you edited a repo something else consumes — the common case; every non-terminal repo here feeds at least the terminal. → **Tier 2**. A per-repo check cannot catch this: a consumer evaluates against its _locked_ input, not your local edit.

| Tier                                                 | Command                                                                               | What it does                                                                                                                                                         |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **1 — contained**                                    | `nix flake check` in each edited repo (add `nix build .#<pkg>` for a changed package) | checks only that repo's own outputs against its locked inputs; touches the fewest derivations, so concurrent runs on different repos barely overlap                  |
| **2 — cross-repo** _(default for code changes here)_ | `pn workspace flake-check`                                                            | runs `nix flake check` in every repo with `--override-input` pinning siblings to your local clones — catches consumer-side breakage without building the host system |
| **3 — system / pre-apply**                           | `pn workspace format` _(optional `nix fmt`)_, then `pn workspace build`               | builds the full host darwin system — the only tier that realizes the assembled system; reserve it, and do not run it from several sessions at once                   |

If a gate fails the task is not complete: fix it (in this or the consuming project) and re-run that tier. If a higher tier later surfaces breakage a lower one missed, your change's blast radius was larger than you classified — validate at the higher tier from then on. `pn workspace build`/`apply` never run `nix fmt` themselves; run `pn workspace format` first if you want it.

After passing a tier, run `pn workspace doctor` as a final consistency gate before declaring the task done — it verifies that the workspace will build consistently locally and on remote.

## Workspace Lifecycle: Three Commands

A pn workspace is bootstrapped and maintained with three separate commands:

### 1. `pn workspace init`

Scans the workspace root for existing git repos and reconciles them into `pn-workspace.toml`. Config-only: does NOT clone repos and does NOT write a lock file. Run this when repos already exist on disk but are not yet in the TOML config.

### 2. `pn workspace clone`

Clones repos listed in `pn-workspace.toml` that are missing on disk. Idempotent: repos already cloned are skipped. Run after writing or editing `pn-workspace.toml` to get repos onto disk.

### 3. `pn workspace lock`

Evaluates each cloned repo's `flake.nix` inputs, discovers workspace dependency edges (by matching input URLs to workspace repo remote URLs), resolves the terminal repo, and writes `pn-workspace.lock.json` atomically. Exits non-zero and preserves the existing lock file if validation errors occur (e.g. `missing_terminal`, `terminal_not_sink`).

## Config and Lock Files

- **`pn-workspace.toml`**: Declares repos, workspace settings (terminal, apply command, etc.), and hooks. The `workspace.terminal` key names the repo whose flake is the build/apply target.
- **`pn-workspace.lock.json`**: Records dependency edges (consumer → alias → target triples), topological order, per-repo flake paths, and the terminal repo. Written by `pn workspace lock`.

The `input-name` field on `[repos.*]` sections has been removed. Alias names are now derived automatically from each consumer's declared flake input aliases at lock time. If you see an error about `input-name`, remove that field from `pn-workspace.toml`.

## --terminal Flag

Every `pn workspace` subcommand accepts `--terminal <name>` to override `workspace.terminal` for that invocation. This is a persistent flag on the `workspace` group.

- **Required-terminal commands** (build, apply, tree, update): Error with a standard message if no terminal is configured and no flag is given.
- **Optional-terminal commands** (init, clone, lock, rebase, push, format, status, flake-check, pre-commit-check): Warn once and continue if no terminal is configured.

## In-Memory Lock Fallback

Commands that need a topological order (rebase, push, status, flake-check, pre-commit-check, update) derive the order in memory if no lock file exists on disk or the disk lock is stale (does not cover the current repo set). This uses `deriveLock` to evaluate flake inputs and build edges without writing anything to disk. The result is the same as with a current lock file, just slower (requires nix eval per repo). To persist the derived order, run `pn workspace lock`.

## Builds and Validation

| Goal                                       | Use                                    | Don't use                                                                            |
| ------------------------------------------ | -------------------------------------- | ------------------------------------------------------------------------------------ |
| Build the system (current host)            | `pn workspace build`                   | `darwin-rebuild build`, `nix build .#darwinConfigurations.<host>.system`             |
| Activate the system                        | the **user** runs `pn workspace apply` | NEVER invoke from agent context                                                      |
| Run `nix flake check` on a project         | `nix flake check` inside a project dir | (no special wrapper needed)                                                          |
| Run `nix flake check` across every project | `pn workspace flake-check`             | per-repo `nix flake check` (that's the Tier 1 completion gate — see Completion Gate) |
| Build a single package                     | `nix build .#<pkg>` inside the project | (no workspace-level wrapper)                                                         |
| Pre-commit checks across all repos         | `pn workspace pre-commit-check`        | per-repo `pre-commit run --all-files`                                                |
| Update flake locks across all repos        | `pn workspace update`                  | per-repo `nix flake update`                                                          |

## When to Push

You don't need to push branches for builds to work. `pn workspace build/flake-check` operate on the local working tree. Push only when:

- The user explicitly asks.
- The work is ready for review/merge.

A failing remote build is **not** a reason to push agent-only branches.

## Command Surface Cheat-Sheet

Workspace-level (operate on every repo in the workspace):

```text
pn workspace init                        Scan for git repos; reconcile into pn-workspace.toml
pn workspace clone                       Clone repos from pn-workspace.toml missing on disk
pn workspace lock                        Derive and write pn-workspace.lock.json
pn workspace build                       Build the current host's system config
pn workspace apply                       Activate (USER ONLY)
pn workspace pre-commit-check            Run pre-commit checks across all repos
pn workspace flake-check                 Run `nix flake check` across all repos
pn workspace doctor                      Audit workspace against build-equality invariant; optionally repair safe drifts
pn workspace update                      Refresh flake locks across all repos, worktree-isolated by default (terminal required)
pn workspace update --in-place           Same, but directly on primary main (old behavior; required inside a workforest set)
pn workspace upgrade                     Update + apply (USER ONLY for the apply step)
pn workspace upgrade --in-place          Update phase runs directly on primary main instead of in an isolated worktree
pn workspace rebase                      Rebase each repo onto its remote tracking branch
pn workspace rebase <branch>             Rebase each repo onto a local ref (no fetch)
pn workspace format                      Run `nix fmt` in each workspace repo
pn workspace push                        Push each repo (USER-INITIATED ONLY)
pn workspace push --set-upstream         Push and set upstream for repos with no remote branch yet; remote resolved via convention chain
pn workspace push -u                     Same as --set-upstream (short flag)
pn workspace push -u --remote <name>     Same, but override the remote for all repos (skip repo if remote absent)
pn workspace status                      Per-repo working-tree summary
pn workspace tree                        Print the workspace dependency DAG (terminal required)
pn workspace nix -- <nix args>           Run nix with --override-input flags injected
pn workspace discover                    List workspace repos and their paths
pn workspace workforest add <branch>              Create a coordinated workforest set on <branch> (all repos)
pn workspace workforest add <branch> <sha>        Same, starting from <commit-ish>
pn workspace workforest add <branch> --repos a,b  Create a SUBSET set with only the named repo keys
pn workspace workforest add-repo <branch> <repo>     Add one repo to an existing set's membership
pn workspace workforest remove-repo <branch> <repo>  Remove one repo from a set (alias: rm-repo; --force); does NOT delete the branch
pn workspace workforest list               List existing workforest sets
pn workspace workforest remove <branch>    Remove a set (alias: rm); does NOT delete the branch
pn workspace workforest prune              Prune stale git worktree admin entries across all repos
```

All subcommands accept `--terminal <name>` to override `workspace.terminal`.

## `pn workspace update` / `upgrade` — worktree-isolated default

`pn workspace update` (and the update phase of `upgrade`) **runs in worktree isolation by
default**. For each repo in topological order it creates an ephemeral worktree + branch off local
`main`, runs `./update-locks.sh` there, rebases + pushes, fast-forwards the primary `main`, and
removes the worktree on success. The canonical clones and their `main` branches stay free during
the long relock; `main` is only touched by a fast fast-forward at the end.

Key points for agents:

- **Dirty-repo behavior differs by mode.** The default worktree flow does _not_ skip a dirty repo
  upfront — the worktree isolates the primary. A dirty `main` _checkout_ is now **autostashed**
  around the fast-forward (the ff is attempted first; on collision the tracked changes are stashed,
  the ff retried, then the stash re-applied). It only defers if the autostash push fails, the ff is
  genuinely not fast-forwardable (remote advanced/diverged), or the autostash pop conflicts — in
  which case the worktree + branch are left for inspection and the run continues to the next repo.
  `--in-place` retains the old behavior, including the upfront dirty-repo skip.

- **`--in-place` escape hatch.** `pn workspace update --in-place` (and
  `pn workspace upgrade --in-place`) runs the original direct-on-`main` flow. Use it when the
  worktree machinery itself is broken, when relocking inside a coordinated workforest set, or when
  you explicitly want the in-place behavior.

- **Inside a coordinated workforest set, `update` requires `--in-place`.** Running bare
  `pn workspace update` from inside a set is an error. Use `pn workspace update --in-place`,
  which relocks the set's worktrees in place and preserves the set's P1 invariant.

- **Concurrent runs are not coordinated.** Two simultaneous `pn workspace update` invocations in
  the same workspace get **distinct** branch names (the `pn-update/<run-ts>` stamp is a sub-second
  timestamp + PID), so they do not collide at `git worktree add`. They are still unsafe to run
  together: both push to remote `main`, so the second run to reach a given repo's push has it
  rejected (non-fast-forward) and that repo fails. Run updates serially.

### Resuming a left-behind worktree

If a step fails, the repo's worktree and branch are left at
`<root>/.workforests/.pn-update/<repo>-<run-ts>` on branch `pn-update/<run-ts>`. The run summary
names the failed step, the git error, and a recovery hint.

To clean up (discard):

```bash
git worktree remove --force <root>/.workforests/.pn-update/<repo>-<run-ts>
git -C <root>/<repo> branch -D pn-update/<run-ts>
# or prune all stale update worktrees at once:
pn workspace workforest prune
git -C <root>/<repo> branch -D pn-update/<run-ts>
```

### Asymmetric-defer recovery

If a defer occurs _after_ the push (remote `main` already advanced, local `main` still behind),
**reset** local main to the remote — do NOT merge:

```bash
# main IS checked out:
git -C <root>/<repo> reset --hard origin/main

# main is NOT checked out (on another branch):
git -C <root>/<repo> branch -f main origin/main
```

The run summary will call this out explicitly when it detects this state.

Full details: `docs/worktrees.md` (section "per-repo ephemeral update worktrees — the update default")
and ADR 0009 (`docs/adr/0009-pn-workspace-update-worktree-isolation.md`) in the `phillipg-nix-repo-base` repo.

## Environment Variables

Every config-path read in pn goes through an environment variable (with a sensible default) or is computed under `PN_WORKSPACE_ROOT`. This makes it safe to run concurrent smoke-test scenarios in isolated temp directories using `t.Setenv`.

| Variable                      | Default                                             | What it controls                                                                                                                                                                 |
| ----------------------------- | --------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PN_WORKSPACE_ROOT`           | nearest ancestor dir containing `pn-workspace.toml` | Workspace root. All workspace files (`pn-workspace.toml`, `pn-workspace.lock.json`, `pn-workspace.revs.json`) and per-repo subdirectories are resolved relative to this root.    |
| `PN_WORKSPACE_OVERRIDE_PATHS` | (empty)                                             | Comma-separated `name=path` pairs that override where pn looks for a workspace repo on disk. Used by tests and CI to inject fixture repos without modifying `pn-workspace.toml`. |
| `XDG_STATE_HOME`              | `~/.local/state`                                    | Parent directory for the apply-cache state (`zn-self-upgrade/apply/applied-hash/`). Override in tests to isolate state from the real user state dir.                             |
| `NO_COLOR`                    | (unset)                                             | When set to any non-empty value, disables ANSI color codes in `pn workspace tree` output.                                                                                        |

### Workspace root resolution order

`pn workspace` subcommands resolve the workspace root using this priority:

1. `--root <dir>` flag (not exposed on every subcommand, but honored by `openWorkspaceRoot`).
2. `PN_WORKSPACE_ROOT` environment variable.
3. Walk upward from the current working directory until a directory containing `pn-workspace.toml` is found.

Once resolved, `pn` exports the root as both `PN_WORKSPACE_ROOT` and `WORKSPACE_ROOT` into every subprocess it spawns (hooks, `update-locks.sh`, etc.).

**Caveat for coordinated workforest sets:** if `PN_WORKSPACE_ROOT` is already exported pointing at the _canonical_ workspace root, it defeats the cd-into-set model — `pn` reads it first (step 2) and resolves the primary workspace instead of the set. When working inside a set, either **unset** `PN_WORKSPACE_ROOT` (the upward search at step 3 will find the set's own `pn-workspace.toml`) or set it explicitly to the set directory. See [Coordinated Workforest Sets](#coordinated-workforest-sets) below.

## Coordinated Workforest Sets

A **coordinated workforest set** is a directory that acts as a complete, self-contained workspace whose repos are git worktrees — all on the same feature branch. It lets an agent work a cross-repo feature in isolation without touching the canonical checkouts.

### How a set is laid out

```text
<canonical_root>/.workforests/<branch>/    # location set by workforests_dir (default .workforests)
├── pn-workspace.toml                    # copied from canonical
├── pn-workspace.lock.json               # copied from canonical
├── pn-workspace.revs.json               # copied; rewritten here by `update`
├── phillipg-nix-repo-base/              # git worktree @ <branch>
├── phillipgreenii-nix-support-apps/     # git worktree @ <branch>
└── …                                    # one worktree per repo in the config
```

By default a set contains **every** repo listed in `pn-workspace.toml`. Pass `--repos <keys>` to `pn workspace workforest add` to create a **subset** set holding only the named repo keys; the set's own `pn-workspace.toml` records that membership (the canonical config is untouched). A workspace dependency excluded from the subset resolves against its locked flake input rather than a set-internal `--override-input`, and a notice names each such consumer→dependency edge. Use `add-repo` / `remove-repo` to change a live set's membership after creation. Directory names match the `[repos.<key>]` map keys exactly.

### The cd-into-set workflow

```bash
# From the canonical workspace root:
pn workspace workforest add my-feature          # create the set; all repos on my-feature
cd .workforests/my-feature                      # enter the set
# unset PN_WORKSPACE_ROOT if it was pointing at the canonical root
unset PN_WORKSPACE_ROOT

# All normal pn workspace verbs now operate on the set's worktrees:
pn workspace status
pn workspace build
pn workspace rebase main                      # rebase each worktree's branch onto local main
pn workspace push --set-upstream              # publish my-feature to origin for the first time
pn workspace update --in-place                # relock inside the set (bare `update` is refused in a set)
```

The set is itself an ordinary workspace root. `pn workspace` verbs "just work" because upward search finds the set's own `pn-workspace.toml` and all repo paths resolve to `{set}/{repo}`. No command-specific worktree logic exists — the verbs are unchanged.

### `pn workspace workforest` commands

| Command                                               | What it does                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pn workspace workforest add <branch> [<commit-ish>]` | Create a set under `workforests_dir/<branch>`. Pre-flights: every repo must exist on disk; set dir must not exist; `<branch>` must not be checked out in any other worktree. If `<branch>` does not exist it is created from `<commit-ish>` (default: canonical `HEAD`), mirroring `git worktree add`. Add `--repos <keys>` (comma-separated or repeated) to create a **subset** set with only the named repos; excluded workspace deps resolve against their locked flake input (a notice names each consumer→dependency edge). |
| `pn workspace workforest add-repo <branch> <repo>`    | Add a single repo to an existing set, recording it in the set's own `pn-workspace.toml`. Use to grow a subset set after creation.                                                                                                                                                                                                                                                                                                                                                                                                |
| `pn workspace workforest remove-repo <branch> <repo>` | Remove a single repo from a set (alias: `rm-repo`; `--force` for dirty/locked). Updates the set's membership. **Does NOT delete the branch.**                                                                                                                                                                                                                                                                                                                                                                                    |
| `pn workspace workforest list`                        | List existing sets under `workforests_dir`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `pn workspace workforest remove <branch>`             | Remove all worktrees in the set and delete the set directory. Alias: `rm`. Mirrors `git worktree remove`: refuses dirty/locked worktrees unless `--force`. **Does NOT delete the branch.**                                                                                                                                                                                                                                                                                                                                       |
| `pn workspace workforest prune`                       | Run `git worktree prune` in every canonical repo, clearing stale `.git/worktrees` admin entries left when a set was deleted manually or a partial `add` failed.                                                                                                                                                                                                                                                                                                                                                                  |

### `rebase [branch]` and `push --set-upstream`

These two enhancements are the natural workflow companions for a fresh set:

- **`pn workspace rebase`** (no arg) — unchanged: fetches and runs `git pull --rebase --autostash` onto each repo's remote tracking branch. Skipped for repos with no upstream (a freshly `-b`-created branch has none yet).
- **`pn workspace rebase <branch>`** — rebase each repo's current branch onto the given local ref. No fetch. Any git ref works (`main`, `origin/main`, another worktree's branch). Repos where the ref does not resolve are skipped with a notice. Use `pn workspace rebase main` to sync a set's feature branches onto local `main`.
- **`pn workspace push --set-upstream`** (or `-u`) — for repos that have no upstream yet, runs `git push -u origin <current-branch>`. Without the flag, repos with no upstream are silently skipped. This is the explicit one-time step to publish a fresh set's branches; afterwards plain `push`/`rebase`/`update` track normally.

### Landing a set onto `main` locally (manual merge-back recipe)

`pn workspace` has **no** local merge/integrate verb (pg2-fdx0). The documented
integration model is set → `push --set-upstream` → review/merge via remote/PR. To land a
set's work onto each repo's **local** `main` without pushing, run this recipe by hand. P1
(the canonical checkouts' working state) holds until the explicit `merge` step.

```bash
# 1. In the set: rebase every repo's feature branch onto local main (fast-forward-able).
cd .workforests/my-feature && unset PN_WORKSPACE_ROOT
pn workspace rebase main

# 2. In each CANONICAL repo (main is checked out there), fast-forward main to the branch.
#    --ff-only refuses to merge if the branch diverged — re-run step 1 if it does.
for r in <repo-a> <repo-b> …; do
  git -C <canonical_root>/$r merge --ff-only my-feature
done

# 3. Remove the set worktrees (frees the branch from being checked out), then delete the branch.
cd <canonical_root>
pn workspace workforest remove my-feature
for r in <repo-a> <repo-b> …; do git -C $r branch -d my-feature; done
```

Only repos that actually have commits on `my-feature` need the `merge`; for a repo where
`my-feature == main` the `branch -d` alone suffices. Validate with `pn workspace build`
before removing the set. Nothing is pushed — push/PR remains the separate, explicit path.

### `PN_WORKSPACE_ROOT` must be unset (or point at the set)

Because `PN_WORKSPACE_ROOT` is checked before the upward walk, a shell session that already has it pointing at the canonical root will silently operate on the primary workspace rather than the set. Rule:

- **Unset it** (preferred): `unset PN_WORKSPACE_ROOT` — the upward walk from `{set}` finds the set's own `pn-workspace.toml`.
- **Or set it to the set directory**: `export PN_WORKSPACE_ROOT=/path/to/.workforests/my-feature`.

Never run `pn workspace` verbs from inside a set while `PN_WORKSPACE_ROOT` points at the canonical root.

### `WORKSPACE_ROOT` and `update-locks.sh`

After resolving the root, `pn` force-exports `PN_WORKSPACE_ROOT` and `WORKSPACE_ROOT` (set to the resolved root) into every subprocess it spawns. However, `update-locks.sh` **recomputes** `WORKSPACE_ROOT` from its own `SCRIPT_DIR/..` at startup (`WORKSPACE_ROOT="${SCRIPT_DIR}/.."`) — it does not use `pn`'s exported value. This is correct because `pn` invokes `update-locks.sh` with its working directory set to `{set}/{repo}`, so `SCRIPT_DIR` resolves into the set and `SCRIPT_DIR/..` is the set root.

**Consequence for hook and script authors:** do not hard-code or rely on `PN_WORKSPACE_ROOT`/`WORKSPACE_ROOT` being stable if you recompute the workspace root from `SCRIPT_DIR`. Ensure your script's `SCRIPT_DIR` is inside the set (i.e. workspace-relative, not an absolute canonical path) so the recomputed root stays within the set.

### Absolute paths in hooks — stay workspace-relative

Hooks copied into the set (`{set}/pn-workspace.toml` carries the hook config; the hook scripts live under `{set}/{repo}`) fire with set-root semantics. A hook that builds paths as `{root}/{repo}` (where `root` is resolved from cwd or `PN_WORKSPACE_ROOT`) stays within the set and respects P1. A hook that **hard-codes an absolute canonical path** (e.g. `/Users/me/workspace/phillipg-nix-repo-base/...`) escapes the set and may violate P1.

**Rule:** hooks must use workspace-relative path construction, not hard-coded absolute canonical paths.

### P1 — the primary checkouts are never modified

**P1 guarantee:** no `pn workspace` verb run from inside a set modifies the canonical (primary) checkouts' working state. Specifically, for every canonical checkout `{canonical_root}/{repo}`: its `HEAD`, checked-out branch, index, and working-tree files are unchanged, and no entry is added to its HEAD/branch reflog.

This holds **structurally**: when `pn` is rooted at a set, every repo path it constructs is `{set}/{repo}`. The canonical path `{canonical_root}/{repo}` is never produced — no verb can address what it never constructs.

**Deliberate carve-out:** `update`/`rebase` run `git fetch`/`git pull` on the set's worktrees, which updates **shared** remote-tracking refs (`refs/remotes/origin/*`) and `FETCH_HEAD`. These are observable from the canonical checkout but never alter its working tree, index, HEAD, or checked-out branch. New commits and branches created in the set land in the shared object store (expected — that is the feature work). P1 protects the primary's **working state**, not the shared object store.

**Practical meaning for agents:** you can work a cross-repo feature branch in full isolation. Running `build`, `update`, `rebase`, `push`, `format`, `flake-check`, and all other verbs from inside the set will not disturb what is checked out in the primary workspace.
