# pn workspace worktree subsetting Implementation Plan

> **For agentic workers:** implement task-by-task with TDD. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let a coordinated worktree set contain a SUBSET of the workspace repos (not always all), and let individual repos be added to / removed from an existing set.

**Architecture:** A set is already "just a workspace whose root is the set dir." We extend that: the set's OWN `pn-workspace.toml` (copied into the set) is the membership store — for a subset it lists only the chosen repos. Because every verb derives paths from `ws.root` and the set's config, no verb needs subset-aware logic. The work is at SET-CONSTRUCTION time: filter the copied config/lock to the member set, and add `worktree add-repo` / `worktree remove-repo` subcommands that mutate a live set's membership + worktrees safely. P1 holds structurally (all paths are `{set}/{repo}`; canonical clones are only read or `git worktree add/remove`-ed against, never mutated in working state).

**Tech Stack:** Go (cobra CLI), `modules/pn/internal/workspace`, smoke-test framework (`internal/workspace/smoke`).

## Global Constraints

- P1 invariant: no `pn workspace` verb run from inside a set may modify a canonical checkout's working state (HEAD/branch/index/working tree/reflog). `git worktree add/remove` against a canonical repo is the permitted carve-out (it touches the shared object store + admin entries, not the canonical working tree).
- Directory names MUST equal repo keys (`[repos.<key>]`).
- Mirror `git worktree add/remove` preflight/force semantics, matching existing `WorktreeAdd`/`WorktreeRemove`.
- Tests MUST be isolated (temp dirs); follow existing `worktree_test.go` FakeRunner patterns and s24–s29 smoke patterns.

## Settled mechanism decisions (deferred per bead, settled here)

1. **Membership store = the set's own `pn-workspace.toml`.** A subset set's copied TOML contains only the chosen repos (canonical TOML untouched → "membership tracked distinctly"). Lock/revs are filtered to the same member subset so `lockMatchesConfig` holds.
2. **Subset selection at create:** `worktree add <branch> --repos a,b,c` (repeatable/comma-list). Empty/absent → all repos (unchanged default).
3. **add/remove individual repos:** `worktree add-repo <branch> <repo>` and `worktree remove-repo <branch> <repo>` operate on an existing set, mutating the set's `pn-workspace.toml` (+ lock/revs filter) and the one worktree.
4. **Subset-dep / lock-edge policy:** When a member repo's workspace dependency is EXCLUDED from the set, the override edge is dropped from the set's lock so nix resolves that input against the repo's own locked/published flake input (canonical), and a clear stderr notice names the consumer→dep fallbacks. This is deterministic and actionable. (Already partially enforced by `overrideInputArgsFor`'s `dirExists` guard; we make it explicit by filtering lock edges/order to member repos at set construction.)

## File Structure

- `internal/workspace/worktree.go` — add `Repos []string` to `WorktreeAddOptions`; add `memberRepos()` selection + validation; add config/lock filtering on copy; add `WorktreeAddRepo`/`WorktreeRemoveRepo` + options. (Primary change.)
- `internal/workspace/config.go` — add `FilterRepos`/marshal helper to write a subset TOML; `MarshalConfig`.
- `internal/workspace/lock.go` (or derive_lock.go) — add `filterLock(member set)` helper.
- `internal/cli/workspace.go` — wire `--repos` flag on `add`; add `add-repo`/`remove-repo` subcommands.
- `internal/workspace/worktree_test.go` — unit tests for subset add, filtering, add-repo, remove-repo, subset-dep notice.
- `internal/workspace/smoke/scenarios/s34-worktree-subset/` + `s35-worktree-add-remove-repo/` — smoke scenarios; register + assert in `smoke_test.go` / `smoke_bare_remote.go`.
- `docs/worktrees.md` — replace the "all repos" caveat with subset behavior.
- pn-workspace-rules SKILL.md note — out of agent worktree (marketplace path); leave a TODO in finish notes.

---

### Task 1: Subset selection on `worktree add`

**Files:** Modify `internal/workspace/worktree.go`, `internal/workspace/config.go`, `internal/workspace/lock.go`; Test `internal/workspace/worktree_test.go`.

- [ ] Add `Repos []string` to `WorktreeAddOptions`.
- [ ] Add `(w) memberRepos(ctx, requested []string) ([]string, error)`: empty → all (topoAlpha); else validate each requested key exists in config, error naming unknown keys; return in topoAlpha order filtered to the subset.
- [ ] `WorktreeAdd` uses memberRepos for preflight + worktree add loop. After git adds, write a FILTERED `pn-workspace.toml` (only member repos, preserving `[workspace]`) and FILTERED lock/revs into the set, instead of verbatim copy when subset; verbatim when all.
- [ ] Add `filterLock(lock, members) *Lock` (order/repos/edges restricted to members; drops edges whose Target excluded) and `MarshalConfig`/`FilterReposConfig`.
- [ ] TDD: test subset add (only chosen repos get worktrees + set TOML lists only them); test unknown-repo error; test all-repos default unchanged (verbatim copy path).

### Task 2: Subset-dependency notice

- [ ] When a dropped edge (Target excluded but Consumer included) exists, write a stderr notice naming consumer→dep fallbacks.
- [ ] TDD: subset where consumer keeps an excluded dep → notice emitted, edge absent from set lock.

### Task 3: `worktree add-repo` / `worktree remove-repo`

**Files:** Modify `worktree.go`, `internal/cli/workspace.go`; Test `worktree_test.go`.

- [ ] `WorktreeAddRepoOptions{Branch, Repo}`, `WorktreeAddRepo`: preflight (set exists, repo in canonical, repo not already in set, branch checked-out check), `git worktree add` the one repo on the set's branch, then re-filter set TOML/lock/revs to include it.
- [ ] `WorktreeRemoveRepoOptions{Branch, Repo, Force}`, `WorktreeRemoveRepo`: preflight (set exists, repo currently in set), `git worktree remove` (mirror force), then re-filter set TOML/lock/revs to drop it. Refuse removing the last repo / leaving inconsistent state.
- [ ] CLI: `add-repo <branch> <repo>`, `remove-repo <branch> <repo>` (alias `rm-repo`), `--force` on remove-repo, `--repos` on add.
- [ ] TDD: add-repo happy path + already-present error; remove-repo happy path + force + not-in-set error + refuse-last-repo.

### Task 4: Smoke + docs

- [ ] s34 subset create; s35 add-repo+remove-repo round trip; register + assert (incl. P1 primary-unchanged spot check).
- [ ] Update `docs/worktrees.md`: remove all-repos caveat, document `--repos`, `add-repo`, `remove-repo`, subset-dep policy.

### Validation

- [ ] `go test ./...` in `modules/pn` green.
- [ ] `nix flake check` in the worktree.
- [ ] prek/pre-commit if present. Do NOT run `pn workspace build`.
