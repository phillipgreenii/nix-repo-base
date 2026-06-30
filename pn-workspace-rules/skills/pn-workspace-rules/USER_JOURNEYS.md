# `pn workspace` — User Journeys

Each journey describes a concrete thing a user does with `pn workspace`,
the commands involved, the expected outcomes (success and error), and the
smoke scenario (`Sx`) that exercises it. Journeys without a smoke scenario
are marked **GAP** with rationale.

Scope: `pn workspace`. The `pn store` and `pn osx` subtrees are out of
scope here (different domains; covered separately if at all).

---

## Setup & Bootstrap

### J1. First-time bootstrap from a hand-written TOML

**Trigger:** user creates a new workspace dir, writes `pn-workspace.toml`
listing the repos by URL, has no repos on disk yet.

**Commands:**

```
pn workspace init    # reconciles config (no-op when toml is hand-written and complete)
pn workspace clone   # fetches every listed repo into the workspace dir
pn workspace lock    # discovers edges, resolves terminal, writes pn-workspace.lock.json
```

**Outcomes:**

- Success: `pn-workspace.lock.json` present with exact `terminal`, `order`,
  `repos`, `edges`; every repo cloned; second `lock` is byte-identical.
- Error: any `ValidationError` from `lock` exits non-zero, preserves any
  existing lock, prints the error to stderr.

**Smoke:** **S1 `fresh-bootstrap`** (asserts every field of lock JSON,
plus byte-identical re-run).

---

### J2. Bootstrap from pre-existing on-disk repos

**Trigger:** user has manually cloned repos into the workspace dir; wants
pn to take over without re-cloning.

**Commands:**

```
pn workspace init    # walks the dir, populates pn-workspace.toml from discovered .git URLs
pn workspace lock    # `clone` is unnecessary; the repos are already present
```

**Outcomes:**

- Success: TOML reflects the on-disk state; `lock` produces a valid
  lockfile.
- Error: `init` is documented as "never errors on indeterminacy" — it
  writes what it can and leaves gaps for the user to fill.

**Smoke:** **GAP**. S1 starts from an empty dir; no scenario currently
covers "repos already on disk, no toml yet". _Recommend:_ add S18.

---

### J3. Add a new repo to an existing workspace

**Trigger:** user wants to bring a new repo into a working workspace.

**Commands:**

```
# edit pn-workspace.toml: add [[repos]] entry
pn workspace clone   # fetches the new repo
pn workspace lock    # re-derives edges; updates pn-workspace.lock.json
```

**Outcomes:**

- Success: new repo is cloned, lock includes it in `repos` and any
  flake-input edges it introduces.
- Error: clone failure (network, auth, bad URL), validation error if the
  new repo's URL collides with an existing one.

**Smoke:** **GAP**. S7 covers idempotent re-run of an unchanged workspace;
no scenario adds a repo mid-flight. _Recommend:_ add S19.

---

### J4. Remove a repo from the workspace

**Trigger:** repo is no longer needed.

**Commands:**

```
# edit pn-workspace.toml: delete the [[repos]] entry
# manually delete the repo directory (pn never deletes for you)
pn workspace lock    # re-derives without the dropped repo
```

**Outcomes:**

- Success: lock no longer references the dropped repo; downstream commands
  no longer iterate over it.
- Error: if the dropped repo was the configured terminal, `lock` errors
  with the terminal field unresolvable.

**Smoke:** **GAP**. No scenario currently exercises removal. _Recommend:_
add S20.

---

## Day-to-day work

### J5. Build the workspace

**Trigger:** user has made code changes; wants to confirm everything still
compiles cross-repo.

**Commands:** `pn workspace build`

**Outcomes:**

- Success: the terminal's nix build succeeds with `--override-input`
  args injected for workspace producers; exits 0. Nix prints
  `warning: not writing modified lock file` (one line per overridden
  input) on this success path — it is benign and expected, not an
  error. See **Expected (Acceptable) Warnings** in `SKILL.md`.
- Error: nix build failure; exits non-zero with build output on stderr.

Note: `pn workspace build` does NOT run `nix fmt`. To format all repos
before building, run `pn workspace format` first.

**Smoke:** ✓ **S18 `happy-path-build`** — two-repo file:// bare-remote
fixture; `build_command = "./build.sh"` (noop script that writes
`built.txt`); asserts exit 0 and marker file in terminal (consumer) dir.
No formatter step in the build (nix fmt coupling removed in tc-perh.9.27).
Note: `pn workspace build` builds the terminal only; the scenario reflects
that (one marker, not two).

---

### J6. Apply NixOS-style configurations

**Trigger:** user wants `nixos-rebuild`-equivalent across repos.

**Commands:** `pn workspace apply`

**Outcomes:** same as `build` but with the configured `apply` command
(typically `nixos-rebuild switch`). Does NOT run `nix fmt`; run
`pn workspace format` first if you want to format. As with `build`, Nix
prints the benign `warning: not writing modified lock file` (one line
per overridden input) on the success path — expected, not an error.
See **Expected (Acceptable) Warnings** in `SKILL.md`.

**Smoke:** ✓ **S19 `happy-path-apply`** — same shape as S18 with
`apply_command = "./apply.sh"`; asserts exit 0 and `applied.txt` in
terminal (consumer) dir. Nix daemon check runs; no formatter step
(nix fmt coupling removed in tc-perh.9.27).

---

### J7. Update dependencies and rebuild

**Trigger:** weekly / periodic refresh of nix flake inputs.

**Commands (default — worktree-isolated):**

```
pn workspace update      # worktree-isolated relock per-repo in topo order (default)
pn workspace apply       # rebuild on the updated locks (USER ONLY)
# or:
pn workspace upgrade     # update + apply in one shot (USER ONLY for apply)
```

**How the default worktree flow works:** for each repo in topo order, `pn workspace update`
creates an ephemeral worktree + branch off local `main` at
`.workforests/.pn-update/<repo>-<run-ts>` on branch `pn-update/<run-ts>`, runs
`./update-locks.sh` there, rebases + pushes, then fast-forwards the primary `main`. The
canonical clones stay free during the long relock; `main` is only touched by a fast fast-forward
at the end.

**Smart integration:** on a clean `main` checkout → `merge --ff-only`; when `main` is not
checked out (working on another branch) → ref-only fast-forward leaving in-progress work
untouched; when `main` is checked out and dirty → defer (worktree + branch left, run continues).

**`--in-place` flag:** runs the original direct-on-`main` flow (including the upfront dirty-repo
skip). Required when calling `update` from inside a coordinated workforest set.

```
pn workspace update --in-place      # old direct-on-main behavior
pn workspace upgrade --in-place     # update phase direct-on-main
```

**Outcomes:**

- Success: every flake.lock advanced; consumer build succeeds; ephemeral worktrees removed.
- Failure (any step): that repo's worktree + branch are left at
  `.workforests/.pn-update/<repo>-<run-ts>` / `pn-update/<run-ts>`. Sweep continues to the next
  repo. End-of-run summary names each repo's outcome, the step that failed, the git error, and a
  recovery hint.
- Deferred (dirty `main` checkout): worktree + branch left; integration skipped for that repo.
- Asymmetric defer (push succeeded but fast-forward failed — remote `main` now ahead of local):
  **reset** local main to the pushed remote, do NOT merge:
  ```
  git -C <root>/<repo> reset --hard origin/main       # when on main
  git -C <root>/<repo> branch -f main origin/main     # when on another branch
  ```
- Error: a flake input no longer resolves; a build fails on the new lock.
- Side effect: `update` appends JSONL events
  (`run_start` / `project_result` / `run_end`) to
  `${XDG_STATE_HOME}/pn/events.jsonl` so downstream tooling can observe
  per-repo timing and exit status. See **J30** for the consumer-side
  journey.

**Resuming a left-behind worktree:**

```bash
# Inspect:
git -C .workforests/.pn-update/<repo>-<run-ts> log --oneline -5

# Clean up (discard):
git worktree remove --force .workforests/.pn-update/<repo>-<run-ts>
git -C <repo> branch -D pn-update/<run-ts>
# or prune all at once then delete branches:
pn workspace workforest prune
git -C <repo> branch -D pn-update/<run-ts>
```

**Concurrent runs:** not coordinated. Two simultaneous `pn workspace update` calls in the same
workspace get **distinct** branch names (the `pn-update/<run-ts>` stamp is a sub-second timestamp +
PID), so they do not collide at `git worktree add`; but both push to remote `main`, so the second
to reach a given repo's push has it rejected (non-fast-forward) and that repo fails. Run updates
serially.

**Inside a coordinated workforest set:** bare `pn workspace update` errors — use
`pn workspace update --in-place`, which relocks the set's worktrees in place.

**Smoke:** ✓ **S20 `happy-path-update`** (now pinned to `--in-place`) — two-repo file:// bare-remote
fixture; each repo's `update-locks.sh` writes `updated.txt` and appends its name to
`$WORKSPACE_ROOT/order.log`; asserts both markers exist and `order.log` records `producer` then
`consumer` (topo order). ✓ **S33 `worktree-update`** (the worktree-isolated default) — a single
`solo` bare-remote repo whose `update-locks.sh` commits a `locked.txt` bump; asserts the relock
commit reached both the primary `main` and the bare remote and that no `.pn-update` worktree
remains afterward.

---

### J8. Inspect workspace state

**Trigger:** "what's going on across these repos right now?"

**Commands:**

- `pn workspace status` — git status per repo, topo order
- `pn workspace tree` — visual DAG rendering
- `pn workspace discover` — list of repos (lighter than `tree`)

**Outcomes:**

- Success: prints to stdout; exit 0.
- Warning (status): no-terminal warning to stderr (does not block).
- Error (tree): required-cmd error if no terminal.

**Smoke:** S15 covers `status` no-terminal warning path; S16 covers `tree`
no-terminal error path; **discover happy-path is GAP**. Happy-path `tree`
and `status` against a valid configured workspace are **GAP** (would need
to assert on rendered output substrings).

---

### J9. Run flake-check / pre-commit-check across repos

**Trigger:** before a commit, validate everything is clean.

**Commands:** `pn workspace flake-check`, `pn workspace pre-commit-check`

**Outcomes:** per-repo nix flake check or pre-commit run; topo order;
warns if no terminal but continues.

**Smoke:** **PARTIAL** via S15 (warning-on-stderr path). Happy-path is
**GAP**.

---

### J10. Push committed changes per-repo

**Trigger:** user has commits across multiple repos and wants them on
their respective remotes.

**Commands:**

```
pn workspace push                        # push repos whose current branch already tracks an upstream
pn workspace push --set-upstream         # also publish branches with no upstream yet
pn workspace push -u                     # short alias for --set-upstream
pn workspace push -u --remote <name>     # same, but override the push remote for all repos
```

**Outcomes:**

- Success (plain `push`): each repo whose current branch has an upstream
  is pushed to its tracked remote in topo order. Repos with no upstream
  (e.g. a freshly created feature branch inside a coordinated workforest
  set — see **J29**) are silently skipped.
- Success (`--set-upstream`/`-u`): for any repo whose current branch has
  no upstream, pn resolves the push remote via this convention chain
  (highest priority first) and runs `git push -u <remote> <current-branch>`:
  1. `--remote <name>` flag — explicit override, applied to every repo.
     If the named remote doesn't exist in a repo, that repo is skipped
     with an error to stderr; the loop continues.
  2. Single-remote shortcut — if the repo has exactly one remote, use it.
  3. `git config branch.<current>.pushRemote` — per-branch push remote.
  4. `git config --local remote.pushDefault` — repo-local default.
  5. `git config --global remote.pushDefault` — user-global default.
  6. `origin` if among the repo's remotes — git's conventional default.
  7. Per-repo error to stderr — skips the repo; the loop continues.

  This is the explicit one-time step to publish a fresh workforest set's
  branches; subsequent `push`/`rebase`/`update` invocations then track
  normally.

- Error: a single repo's _push_ failure stops the chain. Remote
  _resolution_ failures are per-repo skip-and-continue (error to stderr).

To configure a non-origin default push remote for a multi-remote repo:

```
git -C <repo> config remote.pushDefault <name>
```

**Smoke:** ✓ **S21 `happy-path-push`** — two-repo file:// bare-remote
fixture; setup commits a marker file in each workspace clone; `pn
workspace push` advances both bare remotes; asserts each bare remote
HEAD equals the workspace clone HEAD. The `--set-upstream` and
`--remote` variants are **GAP** at the smoke layer (covered by
unit/integration tests only).

---

### J11. Rebase across repos

**Trigger:** working through a stack of cross-repo changes, or syncing a
coordinated workforest set's feature branches onto local `main`.

**Commands:**

```
pn workspace rebase              # fetch + pull --rebase onto each repo's upstream
pn workspace rebase <branch>     # rebase each repo's current branch onto a local ref; no fetch
```

**Outcomes:**

- No-arg form: per-repo `git fetch` + `git pull --rebase --autostash` in
  topo order (as of tc-perh.9.25; no longer requires `git mu` alias).
  Repos with no upstream are skipped with a notice.
- Positional `<branch>` form: per-repo `git rebase <branch>` in topo
  order. **No `git fetch`** is performed — `<branch>` must already exist
  locally (any ref works: `main`, `origin/main`, another set's branch).
  Repos where the ref does not resolve are skipped with a notice and the
  chain continues. Typical use from inside a coordinated workforest set
  (see **J29**): `pn workspace rebase main` to forward-port the set onto
  the canonical workspace's local `main`.

**Smoke:** ✓ **S22 `happy-path-rebase`** and **S22b
`happy-path-rebase-autostash`** — one-repo file:// bare-remote fixture;
workspace reset to commit A while remote is at B; `pn workspace rebase`
advances workspace to B; asserts HEAD matches remote and stash is empty.
S22b additionally seeds a tracked-file modification before rebase and
verifies the autostash round-trip (modification survives, stash empty).
The positional `rebase <branch>` form (no-fetch, missing-ref-skips)
is **GAP** at the smoke layer (covered by unit/integration tests only).

---

### J28. Format the workspace

**Trigger:** user wants to run `nix fmt` across all repos in the workspace
(e.g. before building or committing).

**Commands:** `pn workspace format`

**Outcomes:**

- Success: `nix fmt` runs in each workspace repo in topological+alphabetical
  order; exits 0. Terminal-optional: warns to stderr and continues if no
  terminal is configured.
- Error: first per-repo `nix fmt` failure stops the chain; exits non-zero.

Note: `pn workspace format` is the only `pn workspace` command that runs
`nix fmt`. The `build` and `apply` commands do NOT format automatically.

**Smoke:** ✓ **S23 `happy-path-format`** — two-repo file:// bare-remote
fixture; both repos' flake.nix declares a noop formatter; `pn workspace
format` runs `nix fmt` in each repo; asserts exit 0 and that stdout shows
format banners for both repos in topo order (producer before consumer).

---

### J12. Drop to raw nix with workspace overrides

**Trigger:** user wants to run an ad-hoc nix command (e.g. `nix shell`)
with the workspace's `--override-input` args applied.

**Commands:** `pn workspace nix -- <nix-args>`

**Outcomes:** passthrough; pn injects the override args, then exec's nix.

**Smoke:** **GAP**. No scenario; the passthrough is a documented escape
hatch. _Recommend:_ add S21 asserting the injected `--override-input` args
are correct given a known fixture.

---

## Flag-driven variations

### J13. Override the terminal for one invocation

**Trigger:** user wants to build/apply with a different terminal than
what `workspace.terminal` is set to.

**Commands:** `pn workspace <any-cmd> --terminal <repo>`

**Outcomes:**

- Success: the flag value beats config; flag-resolved terminal is used.
- Persists no state — TOML is not rewritten.

**Smoke:** **S12 `terminal-flag-override`**.

---

### J14. Operate against a workspace with no terminal configured

**Trigger:** initial setup; or a workspace where terminal-detection is
ambiguous.

**Commands:** any `pn workspace <cmd>` without `--terminal` flag, without
`workspace.terminal` in TOML.

**Outcomes:**

- Required commands (`build`, `apply`, `tree`, `update`, `upgrade`):
  error to stderr, exit non-zero. Error includes the candidate list when
  auto-detect produced multiple sinks.
- Non-required commands (everything else): warn to stderr, continue.

**Smoke:** **S15** (warn path on `status`), **S16** (error path on
`build`).

---

## Validation & error journeys

### J15. Migration from `input-name` to per-edge aliases

**Trigger:** user is on an old TOML with `input-name = "..."` fields.

**Commands:** ANY pn command (error fires at `Open()` time during
ParseConfig — every command goes through Open).

**Outcomes:** error to stderr naming the field and the new mechanism
(per-edge aliases derived at lock time); exit non-zero; no state change.

**Smoke:** **S5 `input-name-migration-error`**.

---

### J16. Migration from legacy `pn-workspace.lock` to `.lock.json`

**Trigger:** user upgrades pn; their workspace still has the old
filename on disk.

**Commands:** `pn workspace lock`

**Outcomes:** new `.lock.json` is written; old `.lock` is removed; notice
emitted to stderr naming both filenames. If both exist on entry, the
`.json` wins and `.lock` is still removed.

**Smoke:** **S8 `legacy-lockfile-migration`**, **S8b `both-present`**.

---

### J17. Hand-edited / corrupt lockfile

**Trigger:** user (or a tool) edited `pn-workspace.lock.json` and
violated an invariant (self-edge, duplicate alias, missing endpoint,
empty `flake_path`, terminal not in repos).

**Commands:** ANY pn command (error fires at `Open()` via `ReadLock` →
`ParseLock`, post-tc-perh.9.19).

**Outcomes:** error to stderr naming the invariant and pointing at
`pn workspace lock` to regenerate; exit non-zero.

**Smoke:** **PARTIAL**. Unit tests (`TestOpen_CorruptLockPropagatesError`

- 6 invariant-specific tests in `lock_test.go`) cover all 5 invariants at
  the API level. **No smoke scenario seeds a corrupt `.lock.json` and
  runs the binary against it.** _Recommend:_ add S22 for at least one
  invariant (e.g. self-edge) at the binary boundary, to prove the error
  surfaces to stderr cleanly.

---

### J18. Configured terminal is not actually a sink

**Trigger:** user set `workspace.terminal = "A"` but `A` has inbound
edges from another workspace repo.

**Commands:** `pn workspace lock`

**Outcomes:** error `terminal_not_sink` to stderr naming the offending
repo and the consumer that disqualifies it; exit non-zero; existing valid
lockfile preserved byte-identical.

**Smoke:** **S6 `terminal-not-sink`** (also asserts atomic-write tempfile
cleanup and prior-lock preservation).

---

### J19. Two workspace repos canonicalize to the same URL

**Trigger:** user accidentally lists the same repo under two TOML names.

**Commands:** `pn workspace lock`

**Outcomes:** error `duplicate_remote_url` to stderr naming both repo
config-keys.

**Smoke:** **S11 `duplicate-remote-url`**.

---

### J20. An edge target has no detectable `flake.nix`

**Trigger:** consumer's flake references a workspace producer that has
no `flake.nix` (neither at root nor at `nix/flake.nix`).

**Commands:** `pn workspace lock`

**Outcomes:** error `missing_flake_path` to stderr naming the target
repo and the `pn-workspace.toml` field to set; exit non-zero.

**Smoke:** **S10 `missing-flake-path`** (added by tc-perh.9.24).

---

### J21. Multiple co-equal sink candidates, no terminal configured

**Trigger:** two repos that nobody else depends on; user hasn't picked
a terminal.

**Commands:** `pn workspace lock`

**Outcomes:** error `missing_terminal` to stderr listing every candidate
and the `workspace.terminal` field to set.

**Smoke:** **S9 `missing-terminal-multi-sink`**.

---

## Configuration shape

### J22. Repo's `flake.nix` lives in a subdirectory

**Trigger:** real-world layout where the flake is at `<repo>/nix/flake.nix`
(e.g. `homelab/nix/flake.nix`).

**Commands:** `pn workspace lock`

**Outcomes:** discovery finds the flake at `nix/flake.nix`; lock records
`flake_path: "nix/flake.nix"`. Repos with default-path flakes don't get
`flake_path` written to the TOML.

**Smoke:** **S3 `subdir-flake`**.

---

### J23. Various remote URL forms across the workspace

**Trigger:** real-world workspace mixes `github:`, `https://`, `ssh://`,
`git@host:`, `git+ssh://`, `git+https://`.

**Commands:** `pn workspace lock`

**Outcomes:** every form canonicalizes identically; edge discovery
matches consumer flake input URLs against producer remote URLs
regardless of form.

**Smoke:** **S4 × 6** sub-scenarios — one per URL form.

---

### J24. Repo with multiple remotes

**Trigger:** consumer/producer has a primary `origin` plus a mirror or
secondary remote (e.g. github + gitea).

**Commands:** `pn workspace clone` (and subsequent commands operate on
the cloned repo).

**Outcomes:** clone configures all listed `[[repos.X.remotes]]`; `git
remote -v` shows both.

**Smoke:** **S13 `clone-multi-remote`**.

---

### J25. Topological order ≠ alphabetical order

**Trigger:** consumer name sorts alphabetically BEFORE its producer
(e.g. `aaa` depends on `zzz`).

**Commands:** any command that iterates per-repo.

**Outcomes:** order is `zzz, aaa` (topo) not `aaa, zzz` (alpha). The
shared `topoAlpha()` helper governs every iterator.

**Smoke:** **S2 `topo-not-alpha`** (asserts on `lock.order` directly,
not on the per-iterator behavior — see GAP note).

**GAP within S2:** `rebase`/`push`/`status`/etc. each have their own
per-repo iterator that calls `topoAlpha()`. S2 proves the helper is
correct; it does NOT prove every iterator actually calls it. A
regression where one command bypasses `topoAlpha()` would not be caught
by S2. The in-process integration tests (`integration_test.go`) cover
this via `FakeRunner` order assertions — but that lives in unit tests,
not smoke. _Recommend:_ add per-command iterator-order assertions
either to S2 sub-scenarios or to integration tests if not already there.

---

### J26. Idempotent re-runs

**Trigger:** user runs the bootstrap chain a second time, expects no
diff.

**Commands:** `pn workspace init`, `lock` (each, individually).

**Outcomes:** both files byte-identical to first run; `init` prints
`"no changes"` to stdout.

**Smoke:** **S7 `idempotent-rerun`**, **S14 `init-no-changes-stdout`**.

---

## Discovery / Help

### J27. Look up command help

**Trigger:** new user, or known user trying to remember a flag.

**Commands:** `pn workspace --help`, `pn workspace <cmd> --help`,
`pn --version`.

**Outcomes:** help text mentions the three-command lifecycle
(`init` → `clone` → `lock`) and any envar-overridable config paths.

**Smoke:** **S17 `help-text-snapshot`** (substring assertions on the
lifecycle phrasing).

---

## Coordinated workforest sets

### J29. Coordinated workforest set workflow

**Trigger:** user wants to work a cross-repo feature branch in isolation
from the canonical checkouts — every workspace repo on the same feature
branch, all changes contained under one set directory.

**Commands:**

```
pn workspace workforest add <branch>          # create the set under workforests_dir/<branch>
pn workspace workforest add <branch> <ref>    # same, starting <branch> from <ref> (default: canonical HEAD)
pn workspace workforest list                  # list existing sets
pn workspace workforest remove <branch>       # tear down the set (alias: rm)
pn workspace workforest prune                 # clear stale .git/worktrees admin entries
cd <workforests_dir>/<branch>                 # cd-into-set; normal `pn workspace` verbs now operate on the set
```

The set directory is itself an ordinary workspace root: it carries copies
of `pn-workspace.toml`, `pn-workspace.lock.json`, and
`pn-workspace.revs.json`, plus one git worktree per repo named after the
`[repos.<key>]` map keys. Most `pn workspace` verbs (build, status, rebase,
push, format, …) "just work" inside the set. **Exception:** bare
`pn workspace update` errors inside a set — use
`pn workspace update --in-place` instead (relocks the set's worktrees in
place, preserving the set's P1 invariant).

The set directory location is controlled by the `workforests_dir` field in
`pn-workspace.toml` (default: `.workforests`).

**Outcomes:**

- `add <branch> [<ref>]`: pre-flights that every repo exists on disk,
  the set dir does not exist, and `<branch>` is not already checked out
  in another worktree. If `<branch>` does not exist it is created from
  `<ref>` (default canonical `HEAD`), mirroring `git worktree add`.
- `list`: prints existing sets under `workforests_dir` to stdout.
- `remove <branch>`: tears down every per-repo worktree and deletes the
  set directory. Refuses dirty/locked worktrees unless `--force`. **Does
  NOT delete the branch** — the branch remains for reuse.
- `prune`: runs `git worktree prune` in every canonical repo to clear
  stale `.git/worktrees` admin entries left behind when a set was
  removed manually or a partial `add` failed.
- **P1 invariant:** running any `pn workspace` verb from inside a set
  never modifies the canonical (primary) checkouts' working state —
  their HEAD, branch, index, and working-tree files are untouched. The
  deliberate carve-out is the shared object store and remote-tracking
  refs (`refs/remotes/origin/*`, `FETCH_HEAD`) updated by `update`/
  `rebase`, which are observable from the canonical checkout but never
  alter its working tree.
- Caveat: `PN_WORKSPACE_ROOT` is checked before the upward walk; a shell
  that has it pointing at the canonical root will silently operate on
  the primary workspace from inside a set. Unset it (preferred) or set
  it explicitly to the set directory before running verbs in the set.

**Smoke:** ✓ **S24 `workforest-add`**, **S25
`workforest-add-already-checked-out`** (pre-flight error path), **S26
`workforest-list`**, **S27 `workforest-remove`**, **S28 `workforest-prune`**,
and **S29 `verbs-in-a-set`** — exercise the verb group plus the P1
invariant (canonical checkouts unchanged by verbs run inside a set).
Implementation: `internal/workspace/workforest.go`.

---

## Observability

### J30. Consume the workspace event stream

**Trigger:** tooling (dashboards, CI summaries, agent metrics) wants
machine-readable per-run / per-repo timing and status for workspace
update operations.

**Commands:**

```
pn workspace update               # writes JSONL events as a side effect
tail -F ${XDG_STATE_HOME:-$HOME/.local/state}/pn/events.jsonl   # consumer side
```

**Outcomes:**

- Each `pn workspace update` invocation appends an ordered sequence of
  newline-delimited JSON events to `${XDG_STATE_HOME}/pn/events.jsonl`
  (default `~/.local/state/pn/events.jsonl`). The directory is created
  on first write.
- Event kinds emitted by `update`:
  - `run_start` — one per invocation, at the top of the run.
  - `project_result` — one per repo, in topo order, emitted after that
    repo's update finishes; carries its outcome (`ok`/`failed`/`deferred`),
    the step any failure stopped at, and a recovery note.
  - `run_end` — one per invocation, at the bottom of the run.
- The file is append-only across invocations; consumers should tail or
  seek by offset rather than reread it whole.
- `XDG_STATE_HOME` is honored to keep test runs isolated from the real
  user state dir (the same envar already gates the apply-cache; see the
  Environment Variables section of `CLAUDE.md`).

**Smoke:** **GAP** — no smoke scenario currently exercises the event
stream end-to-end (assert file path, event ordering, schema fields).
Smoke coverage is tracked in **tc-perh.14**. Until that lands, this
journey is covered only by unit/integration tests at the package
boundary.

---

## Coupling summary

**Smoke scenarios (37) cover:** J1, J5–J7, J10–J11, J13–J29 (24 journeys
end-to-end); partial coverage of J8, J9 (via error/warning paths only);
J10 and J11 are full-coverage for the no-flag forms only — the
`push --set-upstream`/`-u` and `rebase <branch>` positional forms are
GAP at the smoke layer (unit/integration coverage only). J30
(events.jsonl) is fully GAP, tracked in tc-perh.14.

**Gaps (journeys without full smoke coverage):**

| journey                                    | gap                                         | smoke proposal | priority |
| ------------------------------------------ | ------------------------------------------- | -------------- | -------- |
| J2 bootstrap from on-disk repos            | smoke starts from empty dir                 | future S       | low      |
| J3 add a repo                              | mid-flight workspace change                 | future S       | medium   |
| J4 remove a repo                           | mid-flight workspace change                 | future S       | low      |
| J8 happy-path discover/tree/status         | rendered-output assertions                  | extend S15/S16 | low      |
| J9 happy-path flake-check/pre-commit-check | same                                        | extend S15/S16 | low      |
| J10 push --set-upstream/-u                 | flag variant not exercised                  | extend S21     | medium   |
| J11 rebase <branch> positional             | no-fetch / missing-ref-skip path not smoked | extend S22     | medium   |
| J12 nix passthrough                        | not covered                                 | future S       | medium   |
| J17 binary-level corrupt-lock              | smoke asserts via unit only                 | future S       | medium   |
| J30 events.jsonl observability             | no end-to-end event-stream smoke            | tc-perh.14     | medium   |

**Closed by tc-perh.9.26 (S18–S22b):**

- J5 build happy-path → **S18** (build_command=./build.sh)
- J6 apply happy-path → **S19** (apply_command=./apply.sh)
- J7 update happy-path → **S20** (update-locks.sh per-repo, topo order.log)
- J10 push happy-path → **S21** (file:// bare remote, HEAD advancement)
- J11 rebase happy-path → **S22 + S22b** (git fetch+pull --rebase --autostash)

**Closed by tc-perh.9.27 (S23):**

- J28 format happy-path → **S23** (nix fmt per-repo, topo order verified via stdout)
- S18/S19 simplified: noop-fmt drv references removed (build/apply no longer run nix fmt)

**Closed by the workforest-set work (S24–S29):**

- J29 coordinated workforest set workflow → **S24 `workforest-add`**,
  **S25 `workforest-add-already-checked-out`**, **S26 `workforest-list`**,
  **S27 `workforest-remove`**, **S28 `workforest-prune`**,
  **S29 `verbs-in-a-set`** (P1 invariant: canonical checkouts
  unchanged by verbs run inside a set).

**Closed by the worktree-isolation-update work (S20 pinned, S33 added):**

- J7 update happy-path (default worktree-isolated flow) → **S33 `worktree-update`**
- S20 `happy-path-update` pinned to `--in-place` flow.

---

_Last updated: 2026-06-24 (worktree-isolated `update` default: J7 and J29 updated to reflect
`--in-place` requirement in sets, new S33 smoke, asymmetric-defer recovery, and concurrent-run
caveat. See ADR 0009).
24 of 30 documented journeys now have full smoke coverage; J30 is GAP, tracked in tc-perh.14._
