# pn workspace — coordinated git worktrees for cross-repo feature work — Design

**Status:** Draft — pending review (bd pg2-i5lf)
**Date:** 2026-06-16
**Repos affected:** `phillipg-nix-repo-base` (`modules/pn`)
**Related:** pg2-xx4g (override build-speed — **landed**, referenced as done), pg2-sc4h (status branch/worktree reporting — sibling), pg2-dirg (add/remove individual repos to/from a set — **deferred follow-up**)

## Problem

`pn`'s `workspace` verbs assume one working copy per repo at the fixed path
`{root}/{repoName}`, all sitting on the configured branch (usually `main`).
Workspace-root resolution (`internal/cli/workspace.go:82`) is just
`PN_WORKSPACE_ROOT` → upward search for `pn-workspace.toml`, and **every** repo
path — including the `--override-input … git+file://{path}` flags that drive
local nix builds (`internal/workspace/helpers.go:82`) — is computed as
`filepath.Join(ws.root, repoName)`.

When a change spans multiple sibling repos, an agent today has to do that work
directly on `main` in each canonical checkout, mutating the primary working
copies and serializing parallel feature work. The team already has a well-worn
single-repo answer (the superpowers `using-git-worktrees` pattern); what's
missing is the **cross-repo analogue**: an isolated set of worktrees — every
repo on a shared feature branch — that the existing `pn workspace` verbs operate
on without disturbing the canonical clones.

The goal Phillip confirmed (pg2-i5lf): _parallel feature work across sibling
repos in isolation; agents do basically what they do today, just across multiple
repos at once._ The hope is this needs **little more than `git worktree` per
repo**.

## The invariant: the primary checkouts are never modified

**P1 — No git state of the canonical checkouts is modified by any operation run
inside a worktree set.** Concretely, for every canonical checkout
`{canonical_root}/{repo}`: its `HEAD`, checked-out branch, index, and
working-tree files are unchanged, and no reflog entry is added to its
HEAD/checked-out branch, by any `pn workspace` verb run from inside a set.

(New commits, branches, and objects in the _shared_ git object store are
expected — that is where the feature work lands — and do not alter the primary
worktree's working directory or its checked-out branch. Worktrees share the
canonical repo's `.git` object store by design, exactly as the single-repo
worktree pattern does.)

**Deliberate carve-out:** `update`/`rebase` run `git fetch`/`git pull` on the
worktrees, which updates _shared_ remote-tracking refs (`refs/remotes/origin/*`)
and `FETCH_HEAD` — observable from the canonical checkout but never altering its
working tree, index, HEAD, or checked-out branch. P1 protects the primary's
**working state**, not the shared object store / remote-tracking refs. The P1
test's allow-list reflects this (see Tests).

P1 is a hard requirement (Phillip, 2026-06-16): it is enforced **structurally**
(every path resolves inside the set — the canonical path is never produced) and
covered by an explicit test.

## Goals

1. Create a **coordinated worktree set** for a feature branch — every repo in the
   workspace config checked out as a worktree on that branch — and run the
   existing `pn workspace` verbs against it, honoring P1.
2. Keep the core `pn` path model essentially unchanged. The generality comes from
   making a set _be_ an ordinary workspace, not from teaching verbs about
   worktrees — **no command-specific (worktree-conditional) logic** (Phillip,
   2026-06-16). Nothing is deferred at the verb level.
3. Enhance `pn workspace rebase` to take an optional `[branch]` argument so a set
   can be rebased onto a **local** branch (e.g. local `main`, or another
   worktree's branch), not only the remote upstream. This is a _general_ feature,
   not worktree-conditional.
4. Let the workspace config specify where worktree sets live (`worktrees_dir`,
   default `.worktrees`).
5. Smoke-test every `pn workspace worktree` subcommand and the verbs running
   inside a set, including a P1 check.
6. Hand off cleanly: enumerate follow-up implementation beads.

## Non-Goals

- Implementing the verbs or writing Go in this bead — this is a design only.
- Changing default single-root behavior. Absent a worktree set, `pn` behaves
  exactly as today; the feature is additive and opt-in.
- **Subsetting a worktree set.** A set always contains _all_ repos in the config.
  Creating a set with only some repos, or adding/removing a repo to/from an
  existing set, needs extra infrastructure and is **deferred to pg2-dirg** — the
  big win (isolated cross-repo work) is delivered by all-repos sets.
- A multi-branch matrix per repo. A set pins **one** feature branch shared across
  its repos (the cross-repo analogue of one feature worktree).
- Remote/PR orchestration (creating PRs, cross-repo merge).

## Design

### Core model: a set is a complete, self-contained workspace of worktrees

Because root resolution only needs a `pn-workspace.toml` to exist, and because
every repo path derives from `ws.root`, a worktree set is **a directory that is
itself an ordinary, valid workspace root** — one whose repos are git worktrees,
all on the shared feature branch:

```
{canonical_root}/.worktrees/<branch>/        # location is config'd; .worktrees is the default
├── pn-workspace.toml                         # copied from canonical (all repos)
├── pn-workspace.lock.json                    # copied from canonical (DAG: order/edges/terminal)
├── pn-workspace.revs.json                    # starts as a copy; update rewrites THIS one
├── phillipg-nix-repo-base/                    # worktree @ <branch>
├── phillipgreenii-nix-support-apps/           # worktree @ <branch>
├── phillipg-nix-ziprecruiter/                 # worktree @ <branch>  (the terminal)
└── …                                          # a worktree @ <branch> for EVERY repo in the config
```

Two rules make a set complete and self-contained:

1. **A set contains a worktree for every repo in `pn-workspace.toml`,** each
   checked out on the shared feature `<branch>`. No subsets, no detached
   checkouts, no special roles — uniform. (Subsetting is pg2-dirg.)
2. **The set's config/lock/revs are copied from canonical,** so the set is a
   valid workspace in its own right.

**Directory names must equal the repo keys** (the `[repos.<key>]` map keys, e.g.
`phillipg-nix-repo-base`, not a shortened alias). Both `filepath.Join(ws.root,
key)` and `update-locks`' sibling resolution (`determine-ul-lib-dir.sh` hardcodes
`${WORKSPACE_ROOT}/phillipg-nix-repo-base/…`) depend on the on-disk name matching
the key.

An agent `cd`s into the set directory. Upward search finds _this_
`pn-workspace.toml` first, so `ws.root` becomes the set and
`filepath.Join(ws.root, repo)` — for clones _and_ `git+file://` override-input
targets — already points at a worktree.

### Why this needs almost no core code change

Verified against the code: a complete set satisfies the existing path model with
**no resolver and no fallback** — every relevant site already joins `ws.root`
with a repo key, and in a set `ws.root` is the set:

- `overrideInputArgsFor` (`helpers.go:82`) → overrides resolve to set worktrees.
- `resolveFlakePath` (`flake_path.go:36`), `Status` (`status.go:30`), `Update`
  (`update.go:104`), `Build`/`Apply` terminal dirs (`build.go:29`,
  `apply.go:34/96`), and the push/rebase/format/flake-check/tree/discover loops
  all join `ws.root` with a repo key → set-internal.
- `Open` loads config/lock/revs from `ws.root` (`workspace.go:33/42/46`); the
  set's own files. `WriteRevLock` (`update.go:172`) → the set's revs, never
  canonical's.
- The copied lock validates without disk access (`lock.go:69`) and matches the
  copied config (`lockMatchesConfig`, `derive_lock.go:117`); since the set
  contains every repo, even regenerating with `pn workspace lock` is safe
  (`gatherInputURLs`, `edges.go:44`, finds every repo).

### How P1 is guaranteed — structurally

The canonical path `{canonical_root}/{repo}` is **never produced** while `pn` is
rooted at a set; every path is `{set}/{repo}`. No verb can address a path it
never constructs, so no verb — current or future — can modify the primary
checkouts, as long as it uses the standard `{ws.root}/{repo}` model. This is why
**no command needs worktree-specific code**. The one thing that _could_ conflict
— two checkouts of the same branch — is prevented at set-creation time (next
section), not papered over with detached checkouts.

### `pn workspace worktree` — the scaffolding verb group

The only meaningful new code besides the rebase enhancement. (A manual
`git worktree add` + copy-config recipe is error-prone and re-done by hand each
time.)

- `pn workspace worktree add <branch> [<commit-ish>]` — create the set under
  `worktrees_dir` (below) for **all** config repos. **Pre-flight checks run first
  and abort before creating anything** (Phillip: "do this check early; that situation
  indicates a larger problem"):
  1. Every config repo exists on disk in the canonical root (else error: run
     `pn workspace clone`).
  2. The set directory `<worktrees_dir>/<branch>` does not already exist.
  3. For every repo, `<branch>` is **not already checked out** in another
     worktree (including the primary). If it is, **error** and name the repo —
     this indicates the branch is in use where it shouldn't be. (`git worktree
add` already fails late with exit 128 on an in-use branch; the pre-flight's
     value is an _early, named, no-partial-state_ error — implement by parsing
     `git worktree list --porcelain` for `branch refs/heads/<branch>`.)

  Then, for each repo, `git -C {canonical}/{repo} worktree add {set}/{repo}
<branch>` — **mirroring `git worktree add` semantics as closely as possible**:
  if `<branch>` exists it is checked out; if not, it is created (`-b <branch>`)
  from the optional `<commit-ish>`, defaulting to the canonical repo's current
  `HEAD` exactly as `git worktree add` does (not a forced `main`). Finally copy
  `pn-workspace.toml` / `.lock.json` / `.revs.json` into the set.

- `pn workspace worktree list` — list sets under `worktrees_dir` with their
  branch.
- `pn workspace worktree remove <branch>` _(alias `rm`)_ — run `git worktree
remove` for each repo's worktree, **mirroring `git worktree remove`**: refuse
  if a worktree is dirty or locked unless `--force`; then delete the now-empty
  set directory. Like `git worktree remove`, this **does not delete the branch**
  it created — leftover branches are the user's to manage, exactly as with a
  normal single-repo git worktree (see "Cost and cleanup").
- `pn workspace worktree prune` — run `git worktree prune` in each canonical
  repo, **mirroring `git worktree prune`**: clean up the administrative
  `.git/worktrees` entries left behind when a set directory was deleted by hand
  (or a partial `add` failed). Worth having because the coordinated model spreads
  worktree admin state across _every_ repo, so a manual `rm -rf` of a set would
  otherwise leave stale entries in N repos to prune one at a time; optionally it
  also removes orphaned directories under `worktrees_dir`.

### `worktrees_dir` config field

Add to the `[workspace]` table (`config.go` `WorkspaceSection`):

```toml
[workspace]
worktrees_dir = ".worktrees"   # default when unset; relative to root, or absolute
```

```go
// WorktreesDir is where `pn workspace worktree` creates sets. Relative paths are
// resolved against the workspace root. Defaults to ".worktrees" when empty.
WorktreesDir string `toml:"worktrees_dir,omitempty"`
```

The default `.worktrees` is dot-prefixed, so `pn workspace init` already ignores
it (`init.go:48` skips `strings.HasPrefix(name, ".")`). **If a non-dot
`worktrees_dir` is configured, the filesystem scanners must skip it** so set
directories aren't mistaken for repos. That is `init` / `reconcileFromFilesystem`
(`init.go:48`, `init.go:289`) only — **`discover` needs no change**: it builds
its list from `ws.config.Repos` keys (`discover.go:32`) and never scans the
filesystem, so it cannot pick up a stray directory. The skip is small, general
workspace-structure awareness, not per-verb logic.

### `pn workspace rebase [branch]` — local-branch rebase target

Today `Rebase` (`rebase.go:26`) runs, per repo with an upstream, `git fetch`
then `git pull --rebase --autostash` — i.e. rebase the current branch onto its
tracking upstream (in the primary, that is remote `main`). That stays the
default. The enhancement adds an optional positional branch:

- `pn workspace rebase` _(no arg — unchanged)_ — `fetch` + `pull --rebase
--autostash` onto each repo's tracking upstream `@{u}`: `origin/main` for a
  primary `main` checkout, `origin/<branch>` for a feature-branch worktree, and
  **skipped entirely** for a freshly `-b`-created feature branch that has no
  upstream yet. (So in a brand-new set, no-arg `rebase` is a no-op until upstream
  is set — see Open items; `rebase main` is the form you'd use to sync onto local
  main.)
- `pn workspace rebase <branch>` — rebase each repo's current branch onto the
  given ref `<branch>`, passed straight to `git -C {repo} rebase --autostash
<branch>` (no fetch/pull). The arg is **any git ref**: a local branch (bare
  `main` = local `main`), another worktree's branch, or an explicit
  remote-tracking ref (`origin/main`) — no special-casing, mirroring git. So
  `pn workspace rebase main` rebases a set's feature branches onto local `main`.
  Repos where the ref doesn't resolve are skipped with a stderr notice
  (preserving the resilient per-repo style).

`RebaseOptions` gains `Onto string`; `workspaceRebaseCmd` takes one optional
positional arg. The behavior keys on the _argument_, not on whether `pn` is in a
worktree — so it is a general feature usable from the primary workspace too, and
introduces no worktree-conditional branching. P1 holds: rebasing _onto_ `main`
moves the feature branch, never `main`, and never the primary's working tree.

### `pn workspace push [--set-upstream]` — publish a fresh set's branch

Today `Push` (`push.go:31`) runs `git push` per repo **only** where the branch
has an upstream (`push.go:38`), skipping the rest. That stays the default: **with
no remote branch, `push` no-ops** — as do no-arg `rebase` and `update`'s push.
This is the intended behavior (Phillip, 2026-06-16): nothing is force-published.

Add an optional `--set-upstream` (`-u`) flag. When set, a repo lacking an
upstream gets `git -C {repo} push -u origin <current-branch>` — publishing the
branch under the **same name** on the remote and recording the upstream. Without
the flag, a no-upstream repo stays skipped. `PushOptions` gains `SetUpstream
bool`; `workspacePushCmd` registers the flag. This is the single explicit step to
publish a fresh set's feature branch; afterwards `push` / `rebase` / `update`
track and push normally. `update` itself does **not** take the flag — publishing
is a deliberate `push --set-upstream`, not a side effect of `update`.

### Per-verb behavior in a set

Every verb other than the rebase enhancement runs **unchanged**:

| Verb                                                                    | In a set                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **build** _(priority)_                                                  | Terminal dir + every `--override-input git+file://` target resolve to set worktrees; `git+file://` keeps the pg2-xx4g cache-stable `dev`-hash, so feature builds stay fast.                                                                                                                                                                                                                                                                          |
| **update** _(priority)_                                                 | Per repo: `git pull --rebase --autostash` **(only if the feature branch has an upstream — skipped on a fresh `-b` branch, gated at `update.go:119`)**, `./update-locks.sh` (relocks against the sibling worktrees in the set), `git push` **(also upstream-gated, `update.go:139`)**, capture rev. Rewrites the **set's** `revs.json`. All paths set-internal → P1 holds.                                                                            |
| **status** _(foundational)_                                             | Reports git status across the set's worktrees. Richer per-repo reporting (branch, ahead/behind, other worktrees) is sibling **pg2-sc4h**.                                                                                                                                                                                                                                                                                                            |
| **rebase**                                                              | Default unchanged; gains `[branch]` (above).                                                                                                                                                                                                                                                                                                                                                                                                         |
| **apply**                                                               | Same override wiring as build, then the configured switch command. One **general** fix: the applied-hash cache keys by `filepath.Base(dir)` = repo name (`updatecache.go:26`), so a set and the primary would share entries — key it by the full repo path (or `ws.root`+name). Correctness only (the cache is XDG state, not a worktree); changing the key one-time-invalidates the primary's hash so the first post-fix apply rebuilds (harmless). |
| **push**                                                                | `git push` per repo with an upstream; no-op where there's no remote branch; `--set-upstream`/`-u` publishes fresh branches (above).                                                                                                                                                                                                                                                                                                                  |
| **format / flake-check / tree / discover / pre-commit-check / upgrade** | Operate on set worktrees. `format`'s `nix fmt` writes only inside set worktrees. `upgrade` = update + apply. All safe by construction; none deferred.                                                                                                                                                                                                                                                                                                |

The setup/utility verbs are not special-cased either: `init` / `clone` / `lock`
operate on the set's own config/repos (a set is created with config/lock copied,
so they are rarely needed _inside_ a set, and `lock` regeneration is safe — see
above), and `nix` is a passthrough using the same `{ws.root}/{repo}` overrides.
Nothing is deferred at the verb level.

### Lock / revs interaction

- `pn-workspace.lock.json` and `pn-workspace.revs.json` are copied verbatim; both
  are fully valid because the set contains every repo. revs is rewritten **inside
  the set** by `update`, so canonical's revs file is never touched (supports P1).
- pg2-xx4g (**landed**): per-source content-digest versioning already decoupled
  the version stamp from the live git rev for `git+file://` overrides, so worktree
  builds inherit cache stability for free.
- Sibling **pg2-sc4h** owns richer per-repo status reporting and will surface
  worktree/branch info inside a set.

## Cost and cleanup

Going all-real-worktrees is simple and uniform, but it is not free, and the doc
should own that:

- **N worktrees per set.** For this workspace that's ~6 working trees created on
  every `worktree add` (disk for 6 checkouts; 6 sequential `git worktree add`).
  The shared object store is reused, so this is checkout cost, not clone cost —
  but it is 6×, not 1×.
- **`update`/`upgrade` run across all N.** `update` relocks (`update-locks.sh`,
  which drives `nix flake update`-style work) and builds/evaluates in _every_
  repo, even ones you never edited — N× the single-repo-worktree cost the design
  cites as precedent. This is the operational price of "no command-specific
  logic": verbs treat the set uniformly.
- **Branch residue (matches git).** `worktree add` creates `<branch>` in every
  repo (via `-b`), and — mirroring `git worktree remove` — `worktree remove`
  deletes the worktree but **not** the branch. So a removed set leaves a branch
  in each canonical repo, exactly as a normal single-repo `git worktree remove`
  would. For untouched repos that branch has zero commits and is harmless; the
  user deletes leftover branches with `git branch -d` as for any worktree, and
  `worktree prune` cleans stale admin entries. A future convenience flag to
  batch-delete a set's branches is possible but intentionally out of scope —
  `remove` follows git.

These costs are acceptable for the stated win (isolated cross-repo work) and are
bounded by the (small) number of workspace repos; the relock-everywhere cost is
the one to revisit if sets grow large.

## Key decisions flagged for review

1. **A set contains all config repos, each as a real worktree on the feature
   branch** (no detached checkouts). Branch-already-checked-out is an _error_
   surfaced early, not something to work around. Subsetting is deferred (pg2-dirg).
2. **Worktree location = `worktrees_dir`, default `.worktrees`** (config-settable,
   resolved against root or absolute).
3. **`rebase [branch]`**: bare branch name = local ref; no arg = remote upstream.
   _(Resolved per Phillip 2026-06-16: this is the intended distinction.)_
4. **New-branch start point follows `git worktree add`** — the canonical repo's
   current `HEAD` by default, or an explicit `[<commit-ish>]`. _(Resolved per
   Phillip: mirror git, not a forced `main`.)_
5. **`worktree remove` mirrors `git worktree remove`** (refuse dirty/locked
   unless `--force`; branch is **not** deleted) and **`worktree prune` mirrors
   `git worktree prune`**. _(Resolved per Phillip.)_

## Edge cases

- **`<branch>` already checked out (primary or another worktree):** `worktree add`
  errors early naming the repo (pre-flight check 3). This is treated as a signal
  of a larger problem, per Phillip.
- **A config repo missing in canonical root:** pre-flight check 1 errors and
  points at `pn workspace clone`; nothing is created.
- **`PN_WORKSPACE_ROOT` set in the environment** overrides upward search and would
  defeat the `cd`-in model — document that it must be unset (or point at the set).
  `openWorkspaceRoot` force-exports `PN_WORKSPACE_ROOT`/`WORKSPACE_ROOT` = set root
  to subprocesses (`cli/workspace.go:75`); note `update-locks.sh` actually
  _recomputes_ `WORKSPACE_ROOT` from its own `SCRIPT_DIR/..` (`update-locks.sh:8`)
  and works only because `pn` runs it with `Dir = {set}/{repo}` (`update.go:133`).
  Cover in the docs bead and the `update` test.
- **Hooks** copied into the set fire with set-root semantics. A hook using the
  standard `{root}/{repo}` model stays within P1; one hard-coding an absolute
  canonical path would not — note the caveat; `worktree add` carries hooks over.
- **Nested cd** (inside `{set}/repo/subdir`) still resolves the set root via
  upward search — correct.

## Tests

- **P1 invariant test (required).** Fixture workspace with N real git repos
  (canonical checkouts on `main` with history). Snapshot each canonical checkout:
  HEAD sha, checked-out branch, `git status --porcelain`, working-tree digests,
  local-branch list, HEAD reflog. Create a set, run **each** verb (status, build,
  update, apply, push, rebase [both forms], format, flake-check, tree, upgrade)
  from inside it; after every verb assert each canonical snapshot is unchanged.
  **Allow-list:** shared remote-tracking refs (`refs/remotes/origin/*`),
  `FETCH_HEAD`, and the shared object store may change (fetch/relock) — excluded
  from the snapshot. Single table-driven test = the structural guarantee made
  executable and a regression net for any future verb.
- **Smoke tests for every worktree command (Phillip, required).** New scenarios
  under `internal/workspace/smoke/scenarios/` (the framework runs the real `pn`
  binary against a scenario workspace, like `s22-happy-path-rebase`):
  - `worktree add` — creates the set, all repos on `<branch>`, config/lock/revs
    copied.
  - `worktree add` against an already-checked-out branch — exits non-zero with the
    early error.
  - `worktree list` — lists the set.
  - `worktree remove` — removes worktrees and the set dir; guards dirty/locked
    (mirrors `git worktree remove`); branch is left behind.
  - `worktree prune` — after a manual `rm -rf` of a set dir, prune clears the
    stale `.git/worktrees` admin entries in each repo.
  - verbs in a set — `status` → `build` → `update` → `rebase main` →
    `push --set-upstream` run from inside a set succeed, and (P1 smoke) the
    primary checkouts are unchanged afterward.
    (The `build`/`update` steps reuse the existing nix-available-or-fake-build-command
    setup that `s18-happy-path-build` / `s20-happy-path-update` use, so the
    scenario doesn't hard-require nix.)
- **rebase unit tests** — no-arg (upstream) path unchanged; `<branch>` path rebases
  onto the local ref with no fetch; missing-local-branch repo is skipped.
- **push unit tests** — no upstream + no flag → skipped (no-op); no upstream +
  `--set-upstream` → `push -u origin <branch>`; existing upstream → plain `push`.
- build: override-input args point at set worktrees (extend
  `override_input_for_test.go`).
- update: worktree pulled/relocked/pushed; the **set's** `revs.json` (not
  canonical's) rewritten.
- apply: applied-hash key distinguishes set from primary.

## Follow-up implementation beads (proposed — to create on approval)

1. **`worktrees_dir` config field** — add to `WorkspaceSection` with the
   `.worktrees` default; make `init`/`reconcileFromFilesystem` skip a non-dot
   value (discover needs no change). _(small; foundation)_
2. **`pn workspace worktree add/list/remove/prune`** — the scaffolding verb group
   with the early pre-flight checks and all config repos; `add` mirrors
   `git worktree add` (HEAD default / optional `<commit-ish>`); `remove` and
   `prune` mirror their git counterparts. _(the bulk of new code)_
3. **`pn workspace rebase [branch]` + `pn workspace push --set-upstream`** — the
   two verb-signature changes for the worktree workflow: `RebaseOptions.Onto` +
   positional arg + local-ref rebase path, and `PushOptions.SetUpstream` +
   `-u`/`--set-upstream` flag; unit tests for both.
4. **P1 invariant test harness** — the table-driven "canonical checkouts unchanged
   after every verb" test. _(gates everything)_
5. **Worktree smoke tests** — a scenario per `worktree` subcommand plus a
   verbs-in-a-set + P1 scenario.
6. **apply cache-key fix** — key the applied-hash by full repo path / `ws.root`.
7. **status/build/update verification in a set** — per-verb tests; coordinate
   status presentation with **pg2-sc4h**.
8. **docs** — agent-facing note on the `cd`-into-set workflow, the
   `PN_WORKSPACE_ROOT`-must-be-unset caveat, the absolute-path-in-hooks caveat,
   and P1 (extend the agent-conventions design / agent rules).

**Deferred (already filed): pg2-dirg** — add/remove individual repos to/from an
existing set (subsetting), which needs extra infrastructure.

## Open items

- The Key decisions above are now resolved per Phillip's direction; nothing there
  is blocking.
- **Fresh-branch upstream — resolved.** A set's `-b` branches have no upstream, so
  no-arg `rebase`, `push`, and `update`'s push **no-op** by design (the intended
  behavior; `hasUpstream` gates them at `rebase.go:33`, `push.go:38`,
  `update.go:139`). Publishing is the explicit `pn workspace push --set-upstream`,
  which sets the upstream to `origin/<branch>`; `update` does not auto-establish
  it.
- **Explicit remote rebase target — resolved.** `rebase <branch>` passes the arg
  to `git rebase` verbatim, so any git ref works (`main`, another worktree's
  branch, or `origin/main`); no separate form is needed.

**No open questions remain — the design is decision-complete and ready to plan.**
