# In-repo per-step update gate + `nvd` apply summary

**Status**: Draft
**Date**: 2026-06-04
**Scope**: `phillipg-nix-repo-base` (`lib/scripts/`, `lib/go-builders.nix`, `modules/pn/default.nix`), all six workspace `update-locks.sh` (+ their helper scripts), and the per-repo `.github/workflows/update-flakes.yml`.

## Context

Two problems with the `pn` workspace workflow.

### Problem 1 — the `nvd` summary never renders after an apply

`pn workspace apply` is supposed to print a package-level diff of the new vs old system generation at the end of a rebuild. `apply.go:86` gates that on `commandExists("nvd")`:

```go
newProfile := readSystemProfile()
if oldProfile != newProfile && newProfile != "" && commandExists("nvd") {
    fmt.Fprintln(out, "  --== Package changes ==--  ")
    _, _ = ws.runner.Run(ctx, "nvd", []string{"diff", oldProfile, newProfile}, ...)
}
```

`nvd` is **not on `PATH`** at runtime (`command -v nvd` → not found), so the diff is silently skipped. The system-profile read itself is correct (`/nix/var/nix/profiles/system` resolves and updates on each apply), and the Go logic is correct. The fault is in packaging: `mkGoBinary` wires `runtimeDeps` via `propagatedBuildInputs` (`go-builders.nix:37`), and `modules/pn/default.nix:20-24` _assumes_ that "surfaces into the profile." It does not — home-manager's `buildEnv` only pulls `nix-support/propagated-user-env-packages`, not `propagated-build-inputs`. So `nvd` never lands on `PATH`. (`git`/`nix` happen to work only because other packages install them.) `makeWrapper` is already imported in `mkGoBinary` (`nativeBuildInputs`, line 36) but unused — wrapping was the original intent, never finished.

### Problem 2 — the time-gate lives outside the repo, so CI and local duplicate work

Each repo's `update-locks.sh` runs a series of steps via `ul_run_step` (in `lib/scripts/update-locks-lib.bash`), each gated by `ul_should_run`/`ul_mark_done` (in `lib/scripts/update-cache-lib.bash`). Today the per-step marker is a `touch`ed file under `$XDG_STATE_HOME/zn-self-upgrade/<repo>/steps/<step>`, and the gate compares its **mtime** against `UL_STALE_SECONDS` (default 12h). `--ci` sets `UL_CI_MODE=true`, which **bypasses the gate entirely**.

Because the marker is machine-local and git doesn't preserve mtimes, there is no shared knowledge of "when was this step last actually run." Consequences:

- CI (`update-flakes.yml`, weekly cron) runs `./update-locks.sh --ci`, which bypasses the gate and always does the work, opens a PR, and auto-merges.
- After CI's commits land on `main`, a local `pn workspace upgrade` pulls them — but the **local** marker is stale/absent, so the local run re-does the exact work CI just did (re-runs `nix flake update`, re-prefetches, re-builds), producing redundant churn.

### Outcome semantics today

`ul_run_step <step_name> <commit_msg> <cmd...>` centralizes all git work; the step command only mutates files and returns an exit code. There are exactly two outcomes:

| Step exit | Framework behaviour                                                          | Marker      |
| --------- | ---------------------------------------------------------------------------- | ----------- |
| `0`       | if tree dirty: `nix fmt` → `git add -A` → `git commit`; then `ul_mark_done`  | touched     |
| non-zero  | `git reset --hard` + `git clean -fd`; record failure (`ul_finalize` exits 1) | not touched |

The user wants a third outcome — "valid attempt, no update applied" — and the timestamp moved into the repo so the gate is shared.

## Decision

### Part 1 — fix `nvd` by wrapping the binary

In `lib/go-builders.nix` `mkGoBinary`, wrap the installed binary so `runtimeDeps` are on its `PATH`, using the already-imported `makeWrapper`:

```nix
postInstall = ''
  … (existing man-page / completions) …
  ${lib.optionalString (runtimeDeps != [ ]) ''
    wrapProgram $out/bin/${name} --suffix PATH : ${lib.makeBinPath runtimeDeps}
  ''}
  ${extraPostInstall}
'';
```

Use `--suffix` (not `--prefix`): runtimeDeps become a **fallback**, so a user's ambient `nix`/`git` still win (important on Determinate Nix — `pn` shells out to the system `nix`), while `nvd` — which isn't ambient — is supplied. Drop `propagatedBuildInputs = runtimeDeps;` — the wrapper script embeds the dep store paths, so Nix's closure scan keeps them retained/GC-rooted without it; keeping it would only add unwanted build-time propagation. Update the now-stale comment in `modules/pn/default.nix`.

This corrects the contract for **every** `mkGoBinary` consumer (`runtimeDeps` = "on `PATH` at runtime"), not just `pn`. See open decision ①.

### Part 2 — in-repo, per-step, three-outcome update gate

#### Storage: per-step plain-text files

Each repo tracks `.update-locks/steps/<step_name>`, each holding a single ISO-8601 UTC timestamp (e.g. `2026-06-04T12:00:00Z`). This mirrors today's `$XDG_STATE_HOME/.../steps/<step>` layout, moved into the repo and storing an explicit **value** (git doesn't preserve mtime). Plain text → **no `jq` dependency** in the core lib (which must still run on the broken-flake fallback path where dev-shell tooling may be absent), trivial pure-bash read/write, and a small per-step conflict surface. `.gitignore` in every repo is clear of `.update-locks/`.

#### The outcome protocol (exit codes)

`ul_run_step` interprets the step command's exit code three ways. The framework keeps owning all git work; steps never call git.

| Step exit                | Meaning                  | Framework behaviour                                                                                                                      | `.update-locks/steps/<step>` |
| ------------------------ | ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| `0`                      | updated                  | `nix fmt` (only if content changed) → write stamp → `git add -A` → **one commit** (content + stamp; or stamp-only if no content changed) | written                      |
| `75` (`UL_RC_ATTEMPTED`) | valid attempt, no update | `git reset --hard` + `git clean -fd` (roll back content) → write stamp → `git add -- .update-locks/steps/<step>` → **stamp-only commit** | written                      |
| any other non-zero       | total failure            | `git reset --hard` + `git clean -fd` (full rollback, incl. any stamp write) → record failure                                             | not written                  |

```bash
UL_RC_ATTEMPTED=75                          # EX_TEMPFAIL — far from generic 1/2 and Nix's 100/101
ul_attempted() { exit "$UL_RC_ATTEMPTED"; }
```

`75` is reserved because it is unambiguous: `git`/`npm`/`go`/`cargo` use `0`/`1` (`git` adds `128`/`129`), and **Nix uses `100`/`101`** for build failure/interruption — so a real tool failure inside a step can never be misread as a deferral. A step emits `75` deliberately via `ul_attempted` (optionally after `echo "WARNING: …"`); a plain command (`nix flake update`) keeps working unchanged (`0`/non-zero).

Both `0` and `75` count as a pass and bump the timestamp. Only "other non-zero" is a hard failure. `ul_finalize` exits 1 **only** when a hard failure occurred — so `pn`'s Go `Update()` (which only sees the script's overall `0`/`1`) is unaffected; `75` never escapes a single step.

#### The gate (`ul_should_run`, rewritten)

- Reads `$_UL_SCRIPT_DIR/.update-locks/steps/<step>` (in-repo, committed), parses the ISO value to epoch via a dual-platform helper (BSD `date -j -u -f …` then GNU `date -u -d …`, mirroring the existing dual-`date` pattern at `update-cache-lib.bash:46-47`), and compares `now - stored ≥ UL_STALE_SECONDS`.
- Missing or unparseable file → **run** (fail-open; worst case re-runs once and re-stamps).
- `NIX_UL_FORCE_UPDATE=true` still forces.
- **`UL_CI_MODE` no longer bypasses the gate** — CI respects the shared, committed timestamps. `UL_CI_MODE` retains only its non-interactive duty: skipping the daemon health-check/prompt in `ul_check_nix_daemon`.

#### New/changed lib functions

- `ul_write_stamp <step>` (new): `mkdir -p .update-locks/steps`; write `date -u +%Y-%m-%dT%H:%M:%SZ` to the step file.
- `ul_should_run`: as above (value-based, in-repo, no CI bypass).
- `ul_mark_done`: removed/folded — stamping now happens inside `ul_run_step`'s commit path via `ul_write_stamp`.
- `ul_run_step`: the three-outcome `case` above.
- `ul_finalize`: add a **Deferred** counter to the summary alongside Ran/Passed/Failed/Skipped; deferrals count as pass.
- `ul_attempted` + `UL_RC_ATTEMPTED`: new.

#### CI (`update-flakes.yml`, all six repos)

CI runs `./update-locks.sh --ci`, now gated by the committed timestamps. Because the cron is weekly and the window is 12h, steps are normally stale → they run, commit (content+stamp, or stamp-only after a deferral/no-op), `HEAD` advances → the existing PR + auto-merge flow shares the new timestamps to `main`. If a local run recently bumped+pushed the stamps, CI finds them fresh and does nothing. No workflow YAML changes are strictly required beyond confirming behaviour; see open decision ② for stamp-only-PR handling.

### Per-step `75` wiring (scope (b) + artifact-404 = `75`)

`75` is opt-in per step. Decision rule, to be documented in `update-locks-lib.bash`:

> Return `75` when the step **fetched** the newer version OK **and updated** the nix files OK, but **running nix (the build) failed** — roll back, warn, defer. Also return `75` when a guessed/derived newer version simply **doesn't exist** (a 404 for something that was expected to exist). Reserve any **other** non-zero for failures **fetching** the info or **updating** the files (transient/unexpected) — those are retried next run and surfaced.

| Repo          | Step(s)                                                        | In-step build today?           | Change                                                                                                                                                                                                                                                             | `75` situation                                 |
| ------------- | -------------------------------------------------------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------- |
| agent-support | `update-deps-{pa-monitor,claude-extended-tool-approver,pg-pr}` | Yes (`nix build .#PKG` verify) | build-fail branch `exit 1` → `exit 75` (`update-deps.sh:55`)                                                                                                                                                                                                       | package won't compile against new deps         |
| support-apps  | `update-deps-jsonl-log-parser`                                 | Yes (`nix build` verify)       | build-fail branch `exit 1` → `exit 75` (`update-deps.sh:28`)                                                                                                                                                                                                       | package won't build against new npm deps       |
| support-apps  | `uv-{pd-schedule-manager,work-activity-tracker}`               | No                             | add `nix build .#<pkg> --no-link` after `uv lock --upgrade`; build-fail → `75`                                                                                                                                                                                     | resolved lock won't build under nix            |
| agent-support | `update-goccc`, `update-toktrack`                              | Yes (opaque)                   | restructure so a fetched-but-won't-compile result → `75` while a fetch/find failure → error (heuristic: nix-update modified version/src but overall failed → `75`; no change at all → error). Exact `nix-update` invocation to be confirmed during implementation. | new rev fetched, build/`cargoHash` fails       |
| overlay       | `update-cmux`, `update-c9watch`, `update-beads-web`            | No                             | before constructing artifact URLs, check the expected asset name is present in the release `assets[]`; real new release with **missing asset** → `75`; asset present but `nix-prefetch-url` fails (server/network/rate-limit) → error                              | new release published, binary not yet uploaded |

**No `75` case (unchanged):** the six `nix flake update` steps (one per repo — no in-step build/guess, so breakage surfaces at apply, as today), `brew-update` (metadata refresh, no repo output), and the three tmux + one bat tip-prefetchers (track branch tips that always exist; failures are network = error). That is 11 steps with a `75` case and 11 without, across all 22.

## Architecture

```
pn workspace upgrade
  └─ Update() (Go)  →  per repo: git pull --rebase → ./update-locks.sh → git push
        └─ update-locks.sh (bash, shared lib)
              ul_setup → ul_run_step × N → ul_finalize
                 └─ ul_run_step:
                      ul_should_run (reads .update-locks/steps/<step>, in-repo value)
                      run step ( set -e; cmd )  →  rc
                        rc 0   → fmt? + write stamp + git commit (content+stamp)
                        rc 75  → reset+clean + write stamp + git commit (stamp-only)
                        rc *   → reset+clean + record failure
  └─ Apply() (Go)  →  nix fmt + (sudo) darwin-rebuild switch
        └─ nvd diff oldProfile newProfile     (now actually on PATH via wrapped pn)
```

Failure isolation layers are unchanged in shape; only `ul_run_step`'s outcome handling and the stamp's location/format change. The apply-side `applied-hash` gate is untouched (see Out of Scope).

## Components

- **`lib/scripts/update-cache-lib.bash`** — rewrite `ul_should_run` (in-repo, value-based, no CI bypass); add `ul_write_stamp`; remove mtime-based `ul_mark_done` and the `$UL_STATE_DIR/.../steps` path. **Remove the vestigial `ul_needs_rebuild`/`ul_mark_applied`** (the apply gate is Go-only, in `updatecache.go`; no live caller in any `.sh`/`.go`) and their ~8 cases in `lib/tests/test-update-cache-lib.bats`. Keep `ul_check_nix_daemon` and the `UL_STATE_DIR` definition (still used for the `pre-commit-drv-path` marker).
- **`lib/scripts/update-locks-lib.bash`** — rewrite `ul_run_step` (three outcomes); add `UL_RC_ATTEMPTED`/`ul_attempted`; add Deferred counter to `ul_finalize`; document the decision rule.
- **`lib/go-builders.nix`** — wrap binary with `runtimeDeps` on `PATH`; drop `propagatedBuildInputs`.
- **`modules/pn/default.nix`** — update the `nvd` comment to reflect wrapping.
- **Per-repo `update-locks.sh` / helpers** — the 11 `75`-wiring changes above. The `step_uv_lock` helper (support-apps) gains a verify build; the three overlay bumper scripts gain the asset-list check; `goccc`/`toktrack` get restructured.
- **`.github/workflows/update-flakes.yml`** (×6) — no gate-bypass flag needed; confirm PR/auto-merge behaviour against stamp-only commits (decision ②).

## Tests

- `lib/tests/test-update-cache-lib.*`: fresh stamp → skip; stale stamp → run; missing/corrupt → run (fail-open); `NIX_UL_FORCE_UPDATE` forces; `UL_CI_MODE` no longer bypasses. Delete the `ul_needs_rebuild`/`ul_mark_applied` cases.
- `lib/tests/test-update-locks-lib.*`: outcome `0` with changes → content+stamp commit; `0` no change → stamp-only commit; `75` → reset+clean then stamp-only commit and counts as pass; other non-zero → full rollback + `ul_finalize` exits 1; Deferred counter increments.
- Per-script: the four `update-deps.sh` build-fail → `75`; uv verify-build → `75`; overlay asset-missing → `75` vs asset-present-fetch-fail → error.

## Migration

First run after deploy: no in-repo stamps exist → every step runs once per machine, commits its stamp, pushes; thereafter shared. No seeding from old XDG markers. Stale `$XDG_STATE_HOME/zn-self-upgrade/<repo>/steps/` dirs can be left or deleted. The shared lib changes in **one** place (`phillipg-nix-repo-base/lib/scripts/`), resolved into all six repos via `determine-ul-lib-dir`.

## Consequences

### Positive

- The time-gate is shared via the repo: after a real update lands (CI PR or local push), every other machine/CI run skips it until the window expires. Local runs no longer redo CI's just-completed work.
- A broken upstream upgrade (Go/npm deps that won't compile, a half-published GitHub release, a Rust crate that won't build) no longer fails the whole upgrade run — it rolls back, warns, defers, and the run still passes.
- `nvd` package diff renders after every apply that changes the system profile.
- Wrapping fixes `runtimeDeps`-on-`PATH` for all `mkGoBinary` consumers.

### Negative

- **Stamp-only commit noise.** A passing step always advances (and commits) its stamp — including a no-op `nix flake update` and `brew update` that changed nothing. So a fully no-op `pn upgrade` after the window still produces one stamp-only commit per due step (bounded: at most once per step per window, since within-window runs skip). On weeks with no real updates, CI may open a PR that only bumps stamps (decision ②).
- **Merge conflicts** on a stamp file are possible when an un-pushed local stamp commit is rebased onto a newer remote stamp for the same step (only on the failed-push path). Resolution is trivial (newer timestamp) but manual; the gate fail-opens on a conflict-marked/unparseable file.
- **`goccc`/`toktrack` restructure** relies on distinguishing `nix-update` failure modes, which it conflates in one exit code; the heuristic (did it modify version/src?) needs validation.
- Factory wrap rebuilds every `mkGoBinary` consumer (e.g. `pa-monitor`); they must be verified to still build.

### Neutral

- `pn upgrade` may build twice for a package that defers (the in-step verify build, then apply's build) — the second is a cache hit.
- The apply-side `applied-hash` gate and the `pre-commit-drv-path` marker remain machine-local.

## Out of Scope

- **Apply rebuild gate (`applied-hash`).** The **Go** gate (`updatecache.go`, used by `apply.go`) tracks _what this machine last switched to_ — inherently per-machine; stays in `$XDG_STATE_HOME`. (The vestigial **bash** `ul_needs_rebuild`/`ul_mark_applied` are removed — see Components.)
- **`pre-commit-drv-path`** install marker — per-machine; stays local.
- **Option (c)** (build-verify the `nix flake update` steps) — explicitly excluded; nixpkgs-bump breakage continues to surface at apply.

## Alternatives Considered

- **Single committed JSON/TOML state file per repo.** Rejected: needs `jq` (absent on the broken-flake fallback) and a conflict-marked file breaks JSON parsing. Per-step plain files avoid both.
- **Helper-function DSL** (`ul_mark_updated`/`ul_mark_attempted`/`ul_fail`). Rejected: steps run in a subshell, so the signal must travel via exit code or a status file anyway; the exit-code convention is the smallest change and keeps bare commands working.
- **Git-history-based freshness** (empty commits + `git log` parsing). Rejected: fragile, unconventional, hard to inspect.
- **`75` scope (a) / (c).** (a) too narrow (leaves obvious deferrable cases as hard failures); (c) expensive and only partially protective for upstream repos. (b) chosen.
- **Scope `nvd` fix to `pn` only** via `extraPostInstall`. Viable, but leaves the same latent bug in other `mkGoBinary` consumers (open decision ①).

## Decisions

1. **`nvd` wrap location** — _resolved: fix the `mkGoBinary` factory._ Corrects `runtimeDeps`-on-`PATH` for all consumers; `--suffix` shadows nothing ambient. Rebuild other consumers (e.g. `pa-monitor`) to verify they still build.
2. **CI stamp-only commits** — _resolved: accept._ The occasional stamp-only PR auto-merges; no special direct-push path.
3. **`applied-hash` gate** — _resolved: stays machine-local._ It records this machine's last-applied system generation (read at `apply.go:72`, written at `apply.go:91`); sharing it in-repo would make other machines skip rebuilds they actually need. The bash `ul_needs_rebuild`/`ul_mark_applied` are vestigial (no live caller) — possible future cleanup, out of scope here.

## Related Decisions

- `phillipg-nix-repo-base/docs/superpowers/specs/2026-05-29-update-locks-resilience-design.md` — per-project/per-step failure isolation and dev-shell re-exec that this builds on.
- `docs/2026-06-01-nixpkgs-26.05-upgrade-migration-plan.md` — the in-flight nixpkgs bump that motivates the deferral semantics.
