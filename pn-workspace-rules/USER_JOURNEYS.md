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
  args injected for workspace producers; exits 0.
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
`pn workspace format` first if you want to format.

**Smoke:** ✓ **S19 `happy-path-apply`** — same shape as S18 with
`apply_command = "./apply.sh"`; asserts exit 0 and `applied.txt` in
terminal (consumer) dir. Nix daemon check runs; no formatter step
(nix fmt coupling removed in tc-perh.9.27).

---

### J7. Update dependencies and rebuild

**Trigger:** weekly / periodic refresh of nix flake inputs.

**Commands:**

```
pn workspace update      # nix flake update per-repo in topo order
pn workspace apply       # rebuild on the updated locks
# or:
pn workspace upgrade     # update + apply in one shot
```

**Outcomes:**

- Success: every flake.lock advanced; consumer build succeeds.
- Error: a flake input no longer resolves; a build fails on the new lock.

**Smoke:** ✓ **S20 `happy-path-update`** — two-repo file:// bare-remote
fixture; each repo's `update-locks.sh` writes `updated.txt` and appends
its name to `$WORKSPACE_ROOT/order.log`; asserts both markers exist and
`order.log` records `producer` then `consumer` (topo order verified).
Consumer's `flake.nix` declares producer as input so the lock detects
the dependency edge.

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

**Commands:** `pn workspace push`

**Outcomes:**

- Success: each repo pushed to its tracked remote in topo order.
- Error: a single repo's push failure stops the chain.

**Smoke:** ✓ **S21 `happy-path-push`** — two-repo file:// bare-remote
fixture; setup commits a marker file in each workspace clone; `pn
workspace push` advances both bare remotes; asserts each bare remote
HEAD equals the workspace clone HEAD.

---

### J11. Rebase across repos

**Trigger:** working through a stack of cross-repo changes.

**Commands:** `pn workspace rebase`

**Outcomes:** per-repo `git fetch` + `git pull --rebase --autostash`
in topo order (as of tc-perh.9.25; no longer requires `git mu` alias).

**Smoke:** ✓ **S22 `happy-path-rebase`** and **S22b
`happy-path-rebase-autostash`** — one-repo file:// bare-remote fixture;
workspace reset to commit A while remote is at B; `pn workspace rebase`
advances workspace to B; asserts HEAD matches remote and stash is empty.
S22b additionally seeds a tracked-file modification before rebase and
verifies the autostash round-trip (modification survives, stash empty).

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

## Coupling summary

**Smoke scenarios (30) cover:** J1, J5–J7, J10–J11, J13–J28 (23 journeys
end-to-end); partial coverage of J8, J9 (via error/warning paths only).

**Gaps (journeys without full smoke coverage):**

| journey                                    | gap                         | smoke proposal | priority |
| ------------------------------------------ | --------------------------- | -------------- | -------- |
| J2 bootstrap from on-disk repos            | smoke starts from empty dir | future S       | low      |
| J3 add a repo                              | mid-flight workspace change | future S       | medium   |
| J4 remove a repo                           | mid-flight workspace change | future S       | low      |
| J8 happy-path discover/tree/status         | rendered-output assertions  | extend S15/S16 | low      |
| J9 happy-path flake-check/pre-commit-check | same                        | extend S15/S16 | low      |
| J12 nix passthrough                        | not covered                 | future S       | medium   |
| J17 binary-level corrupt-lock              | smoke asserts via unit only | future S       | medium   |

**Closed by tc-perh.9.26 (S18–S22b):**

- J5 build happy-path → **S18** (build_command=./build.sh)
- J6 apply happy-path → **S19** (apply_command=./apply.sh)
- J7 update happy-path → **S20** (update-locks.sh per-repo, topo order.log)
- J10 push happy-path → **S21** (file:// bare remote, HEAD advancement)
- J11 rebase happy-path → **S22 + S22b** (git fetch+pull --rebase --autostash)

**Closed by tc-perh.9.27 (S23):**

- J28 format happy-path → **S23** (nix fmt per-repo, topo order verified via stdout)
- S18/S19 simplified: noop-fmt drv references removed (build/apply no longer run nix fmt)

---

_Last updated: 2026-06-14 (tc-perh.9.27 adds S23, simplifies S18/S19: 1 new scenario).
23 of 28 documented journeys now have full smoke coverage._
