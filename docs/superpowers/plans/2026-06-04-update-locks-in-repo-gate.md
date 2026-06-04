# In-repo update-locks gate + `nvd` apply summary — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pn workspace apply` print its `nvd` package diff, and move the per-step update-locks freshness gate from a machine-local mtime marker into a committed, per-step timestamp file so CI and local runs share it — with a three-outcome step protocol (updated / valid-attempt / failure).

**Architecture:** Two independent parts. Part 1 fixes packaging: wrap the `mkGoBinary` output so `runtimeDeps` (incl. `nvd`) land on the binary's `PATH`. Part 2 reworks the shared bash libs in `phillipg-nix-repo-base/lib/scripts/`: `update-cache-lib.bash` reads/writes an in-repo ISO-timestamp per step, and `update-locks-lib.bash`'s `ul_run_step` branches on the step exit code (`0` = commit content+stamp, `75` = roll back content but commit stamp, other = full rollback). All existing steps keep working unchanged; `75` is available but unused until a follow-up plan wires it into individual steps.

**Tech Stack:** Nix (`buildGoModule`, `makeWrapper`/`wrapProgram`), Bash, bats (tests), git.

**Scope note:** This plan covers Part 1 (`nvd`) and the Part 2 **core framework** (shared libs + tests + vestigial removal). The per-step `75` wiring across the six repos' `update-locks.sh`/helper scripts (the 11 step edits in the spec) is a **separate follow-up plan** that depends on this landing.

**Spec:** `docs/superpowers/specs/2026-06-04-update-locks-in-repo-gate-design.md`

---

## File Structure

| File                                   | Responsibility             | Action                                                                                                                                                                |
| -------------------------------------- | -------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lib/go-builders.nix`                  | `mkGoBinary` factory       | Modify: wrap binary with `runtimeDeps` on `PATH`; drop `propagatedBuildInputs`                                                                                        |
| `modules/pn/default.nix`               | `pn` package def           | Modify: update the stale `nvd` comment                                                                                                                                |
| `lib/scripts/update-cache-lib.bash`    | freshness gate + stamp I/O | Modify: in-repo value-based gate; add `ul_write_stamp` + `_ul_iso_to_epoch`; change `ul_init` signature; remove `ul_mark_done`, `ul_needs_rebuild`, `ul_mark_applied` |
| `lib/scripts/update-locks-lib.bash`    | step runner                | Modify: `UL_RC_ATTEMPTED`/`ul_attempted`; three-outcome `ul_run_step`; `_UL_STEPS_DEFERRED` counter; `ul_finalize` summary                                            |
| `lib/tests/test-update-cache-lib.bats` | cache-lib tests            | Modify: rewrite for value-based in-repo gate; delete vestigial-fn tests                                                                                               |
| `lib/tests/test-update-locks-lib.bats` | locks-lib tests            | Modify: add three-outcome + deferred tests; fix tests that used `ul_mark_done` / assumed no-op = no-commit                                                            |

**Active system for build/check commands:** `aarch64-darwin`. All commands below assume CWD is the worktree root (the checkout of this repo).

**Test command — use this everywhere a step says "run the tests":**

```bash
nix build .#checks.aarch64-darwin.test-update-locks-lib -L
```

This runs **both** `lib/tests/*.bats` files in the Nix sandbox (GNU sed, pinned coreutils/git). Notes for whoever executes this:

- **Do NOT use `nix run nixpkgs#bats -- …` on macOS.** The harness helper `_fix_mock_shebang` uses GNU-only `sed -i "1s|…|…|"`; the ambient macOS **BSD sed** misparses it and every mock-using test errors in `setup`. The sandbox check uses GNU sed and is the source of truth.
- **`nix build .#checks…` reads tracked files from the git working tree.** All files this plan edits are already tracked, so working-tree edits are picked up — but if results look stale, `git add -A` first to be certain Nix sees them. (A dirty-tree warning from Nix is expected and harmless.)
- When a step says "verify it FAILS," you'll see the relevant new/changed test fail inside the otherwise-green suite; when it says "verify it PASSES," the whole `test-update-locks-lib` build must succeed.
- Optional faster local loop (only if you want it): `nix shell nixpkgs#bats nixpkgs#gnused nixpkgs#coreutils nixpkgs#git -c env UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" bats lib/tests/<file>.bats` — `gnused` puts GNU `sed` on PATH so `_fix_mock_shebang` works.

---

## Phase 1 — `nvd` apply summary

### Task 1: Wrap `mkGoBinary` output so `runtimeDeps` reach `PATH`

**Files:**

- Modify: `lib/go-builders.nix:37` and `lib/go-builders.nix:80`
- Modify: `modules/pn/default.nix:20-24`

- [ ] **Step 1: Confirm the bug (nvd absent from the current `pn`)**

Run:

```bash
nix build .#pn -L && grep -c nvd result/bin/pn; echo "exit:$?"
```

Expected: `0` matches (current wrapper-less binary is `result/bin/pn` itself, no `nvd` reference). This is the "before" state.

- [ ] **Step 2: Drop `propagatedBuildInputs`, add the wrap to `postInstall`**

In `lib/go-builders.nix`, delete line 37:

```nix
        propagatedBuildInputs = runtimeDeps;
```

Then, in the `postInstall` string, immediately before the final `${extraPostInstall}` (line 80), insert:

```nix
          ${lib.optionalString (runtimeDeps != [ ]) ''
            wrapProgram $out/bin/${name} --suffix PATH : ${lib.makeBinPath runtimeDeps}
          ''}
```

(`makeWrapper` is already in `nativeBuildInputs` at line 36, which provides `wrapProgram`.)

- [ ] **Step 3: Update the stale comment in `modules/pn/default.nix`**

Replace lines 20-24:

```nix
    # `pn workspace apply` runs `nvd diff <old> <new>` to show the generation
    # upgrade comparison, but only when nvd is on PATH (apply.go gates on
    # commandExists("nvd")). Ship it as a runtime dep so the diff actually
    # renders; like git, it surfaces into the profile via propagatedBuildInputs.
    pkgs.nvd
```

with:

```nix
    # `pn workspace apply` runs `nvd diff <old> <new>` to show the generation
    # upgrade comparison, but only when nvd is on PATH (apply.go gates on
    # commandExists("nvd")). mkGoBinary wraps pn with `--suffix PATH` over
    # runtimeDeps, so nvd is reachable at runtime (a user's ambient nix/git
    # still win; nvd, which isn't ambient, is supplied as a fallback).
    pkgs.nvd
```

- [ ] **Step 4: Rebuild and verify nvd is now on the wrapped binary's PATH**

Run:

```bash
nix build .#pn -L && grep -o '/nix/store/[^:"]*nvd[^:"]*/bin' result/bin/pn | head -1
```

Expected: prints a `/nix/store/…-nvd-…/bin` path (the wrapper now injects `nvd` into `PATH`).

- [ ] **Step 5: Verify `pn` itself still runs**

Run:

```bash
result/bin/pn --help >/dev/null && echo OK
```

Expected: `OK` (wrapper exec's the real binary correctly).

- [ ] **Step 6: Rebuild other `mkGoBinary` consumers to confirm the factory change is safe**

Run:

```bash
grep -rl 'mkGoBinary' --include='*.nix' . | grep -v go-builders.nix
```

Expected: lists `modules/pn/default.nix` (and any others). For each _other_ consumer found, build it (e.g. `nix build .#<name> -L`). Expected: all build (wrapping is additive; `--suffix` shadows nothing).

- [ ] **Step 7: Commit**

```bash
git add lib/go-builders.nix modules/pn/default.nix
git commit -m "fix: wrap mkGoBinary output so runtimeDeps (nvd) reach PATH"
```

---

## Phase 2 — In-repo freshness gate (`update-cache-lib.bash`)

### Task 2: ISO↔epoch helper, `ul_init` signature, `ul_write_stamp`

**Files:**

- Modify: `lib/scripts/update-cache-lib.bash`
- Test: `lib/tests/test-update-cache-lib.bats`

- [ ] **Step 1: Replace the cache-lib test setup + write failing tests for the new stamp API**

In `lib/tests/test-update-cache-lib.bats`, replace the `setup()` (lines 12-17) and the `ul_init` tests (lines 30-47) with:

```bash
setup() {
  TEST_DIR=$(mktemp -d)
  export NIX_UL_FORCE_UPDATE="false"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"   # repo dir; stamps live under it
}

@test "ul_init sets _UL_PROJECT and _UL_STAMP_DIR under the repo" {
  [ "$_UL_PROJECT" = "my-project" ]
  [ "$_UL_STAMP_DIR" = "$TEST_DIR/repo/.update-locks/steps" ]
}

@test "ul_write_stamp creates the stamp file with an ISO-8601 UTC value" {
  ul_write_stamp "some-step"
  local f="$TEST_DIR/repo/.update-locks/steps/some-step"
  [ -f "$f" ]
  run cat "$f"
  [[ "$output" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
}

@test "_ul_iso_to_epoch round-trips a known timestamp" {
  run _ul_iso_to_epoch "2021-01-01T00:00:00Z"
  [ "$status" -eq 0 ]
  [ "$output" = "1609459200" ]
}

@test "_ul_iso_to_epoch fails on garbage" {
  run _ul_iso_to_epoch "not-a-date"
  [ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run the new tests; verify they fail**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-cache-lib.bats`
Expected: FAIL — `ul_init` takes one arg today (`_UL_STAMP_DIR` unset), `ul_write_stamp`/`_ul_iso_to_epoch` undefined.

- [ ] **Step 3: Implement the new init + stamp helpers**

In `lib/scripts/update-cache-lib.bash`, replace `ul_init` (lines 13-16) with:

```bash
ul_init() {
  _UL_PROJECT="$1"
  _UL_STAMP_DIR="$2/.update-locks/steps"
}

# Convert an ISO-8601 UTC timestamp ("2026-06-04T12:00:00Z") to epoch seconds.
# Tries BSD date (macOS) then GNU date (Linux). Non-zero exit if unparseable.
_ul_iso_to_epoch() {
  local iso="$1"
  date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$iso" +%s 2>/dev/null ||
    date -u -d "$iso" +%s 2>/dev/null
}

# Write the current time (ISO-8601 UTC) as this step's in-repo stamp.
ul_write_stamp() {
  local step_name="$1"
  mkdir -p "$_UL_STAMP_DIR"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$_UL_STAMP_DIR/$step_name"
}
```

Add `_UL_STAMP_DIR=""` next to the existing `_UL_PROJECT=""` declaration (line 11).

- [ ] **Step 4: Run the new tests; verify they pass**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-cache-lib.bats`
Expected: the four tests from Step 1 PASS. (Other tests in the file still fail — fixed in Task 3/4.)

- [ ] **Step 5: Commit**

```bash
git add lib/scripts/update-cache-lib.bash lib/tests/test-update-cache-lib.bats
git commit -m "feat: in-repo stamp helpers (ul_write_stamp, _ul_iso_to_epoch, ul_init repo arg)"
```

### Task 3: Value-based `ul_should_run` (no CI bypass)

**Files:**

- Modify: `lib/scripts/update-cache-lib.bash` (`ul_should_run`)
- Test: `lib/tests/test-update-cache-lib.bats`

- [ ] **Step 1: Replace the `ul_should_run` / `ul_mark_done` tests (lines 49-134) with value-based ones**

```bash
# --- ul_should_run (in-repo, value-based) ---

@test "ul_should_run returns 0 when no stamp exists" {
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ul_should_run returns 1 when stamp is fresh" {
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 1 ]
}

@test "ul_should_run returns 0 when stamp is stale" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "2020-01-01T00:00:00Z" > "$_UL_STAMP_DIR/some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 0 (fail-open) when stamp is unparseable" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "<<<<<<< HEAD" > "$_UL_STAMP_DIR/some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 0 when UL_FORCE is true even if fresh" {
  export NIX_UL_FORCE_UPDATE="true"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run does NOT bypass on UL_CI_MODE" {
  export UL_CI_MODE="true"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  ul_write_stamp "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 1 ]   # CI now respects the shared gate
}

@test "ul_should_run skip message includes step name, timestamp, and remaining" {
  mkdir -p "$_UL_STAMP_DIR"
  echo "2026-06-04T12:00:00Z" > "$_UL_STAMP_DIR/brew-update"
  # Force "fresh" by setting a huge window so it's always within it.
  export UL_STALE_SECONDS=999999999
  source "$UL_LIB_SCRIPT"
  ul_init "my-project" "$TEST_DIR/repo"
  run ul_should_run "brew-update"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "Skipping brew-update" ]]
  [[ "$output" =~ "2026-06-04T12:00:00Z" ]]
  [[ "$output" =~ "next eligible in" ]]
}
```

- [ ] **Step 2: Run; verify failures**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-cache-lib.bats`
Expected: the new `ul_should_run` tests FAIL (still reading the old mtime/XDG marker; `UL_CI_MODE` still bypasses).

- [ ] **Step 3: Rewrite `ul_should_run`**

Replace `ul_should_run` (lines 18-51) with:

```bash
ul_should_run() {
  local step_name="$1"
  local stamp="$_UL_STAMP_DIR/$step_name"

  if [[ $UL_FORCE == "true" ]]; then
    return 0
  fi
  # NOTE: UL_CI_MODE intentionally does NOT bypass — CI respects the shared,
  # committed gate. UL_CI_MODE only governs the daemon health-check elsewhere.

  [[ -f $stamp ]] || return 0

  local stored_iso stored_epoch now age
  stored_iso=$(<"$stamp")
  stored_epoch=$(_ul_iso_to_epoch "$stored_iso") || return 0   # unparseable → run
  now=$(date +%s)
  age=$((now - stored_epoch))

  if [[ $age -ge $UL_STALE_SECONDS ]]; then
    return 0
  fi

  local remaining=$((UL_STALE_SECONDS - age))
  local hours=$((remaining / 3600))
  local minutes=$(((remaining % 3600) / 60))
  local seconds=$((remaining % 60))
  echo -e "\033[33mSkipping ${step_name}: last successful at ${stored_iso}, next eligible in ${hours}h ${minutes}m ${seconds}s\033[0m"
  return 1
}
```

- [ ] **Step 4: Run; verify the `ul_should_run` tests pass**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-cache-lib.bats`
Expected: all `ul_should_run` tests PASS. (Vestigial-fn tests still present/failing — Task 4.)

- [ ] **Step 5: Commit**

```bash
git add lib/scripts/update-cache-lib.bash lib/tests/test-update-cache-lib.bats
git commit -m "feat: value-based in-repo ul_should_run; CI respects the gate"
```

### Task 4: Remove vestigial functions and their tests

**Files:**

- Modify: `lib/scripts/update-cache-lib.bash` (delete `ul_mark_done`, `ul_needs_rebuild`, `ul_mark_applied`)
- Modify: `lib/tests/test-update-cache-lib.bats` (delete their tests)

- [ ] **Step 1: Delete `ul_mark_done`, `ul_needs_rebuild`, `ul_mark_applied`**

In `lib/scripts/update-cache-lib.bash`, delete the entire `ul_mark_done` function (lines 53-58), the entire `ul_needs_rebuild` function (lines 60-86), and the entire `ul_mark_applied` function (lines 88-97). Keep `ul_check_nix_daemon`, `UL_STATE_DIR` (still used by the locks lib for the `pre-commit-drv-path` marker), `UL_STALE_SECONDS`, `UL_FORCE`, `UL_CI_MODE`.

- [ ] **Step 2: Delete the tests for the removed functions**

In `lib/tests/test-update-cache-lib.bats`, delete the `setup_test_repo` helper and every `ul_needs_rebuild`/`ul_mark_applied` test (the `# --- ul_needs_rebuild / ul_mark_applied ---` block). Keep the `ul_check_nix_daemon` tests.

- [ ] **Step 3: Verify no remaining references anywhere**

Run:

```bash
grep -rn 'ul_mark_done\|ul_needs_rebuild\|ul_mark_applied' lib/ ../*/update-locks.sh
```

Expected: no matches (the locks lib's `ul_mark_done` call is replaced in Task 6; if this grep still shows `update-locks-lib.bash`, that's expected until Task 6 — note it and proceed).

- [ ] **Step 4: Run the full cache-lib test file**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-cache-lib.bats`
Expected: PASS (all remaining tests: stamp helpers, `ul_should_run`, `ul_check_nix_daemon`).

- [ ] **Step 5: Commit**

```bash
git add lib/scripts/update-cache-lib.bash lib/tests/test-update-cache-lib.bats
git commit -m "refactor: remove vestigial ul_mark_done/ul_needs_rebuild/ul_mark_applied"
```

---

## Phase 3 — Three-outcome step runner (`update-locks-lib.bash`)

### Task 5: Add `UL_RC_ATTEMPTED`/`ul_attempted` and the deferred counter

**Files:**

- Modify: `lib/scripts/update-locks-lib.bash`

- [ ] **Step 1: Declare the exit-code contract and counter**

Near the top of `lib/scripts/update-locks-lib.bash` (after the `_UL_LOCKS_LIB_DIR=...` line, line 6), add:

```bash
# Exit code a step returns to mean "valid attempt, no update applied" — roll
# back content but record the timestamp (so it isn't retried until the window
# expires) and keep the run passing. 75 = EX_TEMPFAIL: clear of generic 1/2 and
# of Nix's 100/101, so a real tool failure is never misread as a deferral.
UL_RC_ATTEMPTED=75
ul_attempted() { exit "$UL_RC_ATTEMPTED"; }
```

Add `_UL_STEPS_DEFERRED=0` to the global counter block (after line 11's `_UL_STEPS_SKIPPED=0`).

- [ ] **Step 2: Reset the counter in `ul_setup` and fix the `ul_init` call**

In `ul_setup`, change the `ul_init "$project_name"` call (line 146) to:

```bash
  ul_init "$project_name" "$script_dir"
```

In the counter-reset block (lines 168-172), add after `_UL_STEPS_SKIPPED=0`:

```bash
  _UL_STEPS_DEFERRED=0
```

- [ ] **Step 3: Verify the lib still sources cleanly**

Run:

```bash
nix run nixpkgs#bash -- -c 'source lib/scripts/update-locks-lib.bash; echo "$UL_RC_ATTEMPTED $_UL_STEPS_DEFERRED"'
```

Expected: `75 0`.

- [ ] **Step 4: Commit**

```bash
git add lib/scripts/update-locks-lib.bash
git commit -m "feat: add UL_RC_ATTEMPTED/ul_attempted and deferred counter scaffolding"
```

### Task 6: Three-outcome `ul_run_step`

**Files:**

- Modify: `lib/scripts/update-locks-lib.bash` (`ul_run_step` + two new helpers)
- Test: `lib/tests/test-update-locks-lib.bats`

- [ ] **Step 1: Update the two affected existing tests + add the new-outcome tests**

In `lib/tests/test-update-locks-lib.bats`:

(a) Replace the test `"ul_run_step with no changes does not create commit"` (lines 89-102) with:

```bash
@test "ul_run_step with no content change creates a stamp-only commit" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  local before_hash
  before_hash=$(git rev-parse HEAD)

  noop_step() { true; }
  ul_run_step "noop-step" "update: noop" noop_step

  # HEAD advanced, and the only change is the stamp file.
  [ "$(git rev-parse HEAD)" != "$before_hash" ]
  run git show --name-only --format= HEAD
  [[ "$output" =~ ".update-locks/steps/noop-step" ]]
  [ "$(git show --name-only --format= HEAD | grep -vc '^$')" -eq 1 ]
}
```

(b) Replace the test `"ul_run_step skips cached steps"` (lines 198-209) — change `ul_mark_done "cached-step"` to `ul_write_stamp "cached-step"`:

```bash
@test "ul_run_step skips cached steps" {
  export NIX_UL_FORCE_UPDATE="false"
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  ul_write_stamp "cached-step"
  noop() { true; }
  ul_run_step "cached-step" "msg" noop

  [ "$_UL_STEPS_SKIPPED" -eq 1 ]
  [ "$_UL_STEPS_RAN" -eq 0 ]
}
```

(c) Add a new section after the success-path tests:

```bash
# --- ul_run_step: success commits content + stamp together ---

@test "ul_run_step success commits content and the stamp in one commit" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  my_step() { echo "new content" > file.txt; }
  ul_run_step "test-step" "update: test step" my_step

  [ "$(git log -1 --format=%s)" = "update: test step" ]
  run git show --name-only --format= HEAD
  [[ "$output" =~ "file.txt" ]]
  [[ "$output" =~ ".update-locks/steps/test-step" ]]
}

# --- ul_run_step: deferral (exit 75) ---

@test "ul_run_step exit 75 rolls back content but commits the stamp" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  deferring_step() { echo "junk" > file.txt; echo "WARNING: not ready" >&2; ul_attempted; }
  ul_run_step "defer-step" "update: defer" deferring_step

  # Content rolled back (file.txt back to original), tree clean.
  [ "$(cat file.txt)" = "initial" ]
  git diff --quiet
  git diff --cached --quiet
  # A stamp-only commit landed.
  run git show --name-only --format= HEAD
  [[ "$output" =~ ".update-locks/steps/defer-step" ]]
  [[ ! "$output" =~ "file.txt" ]]
  # Counted as a pass (deferred), not a failure.
  [ "$_UL_STEPS_DEFERRED" -eq 1 ]
  [ "$_UL_STEPS_FAILED" -eq 0 ]
}

@test "ul_run_step exit 75 with no content change still commits the stamp" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  before=$(git rev-parse HEAD)
  defer_noop() { ul_attempted; }
  ul_run_step "defer-noop" "msg" defer_noop

  [ "$(git rev-parse HEAD)" != "$before" ]
  [ "$_UL_STEPS_DEFERRED" -eq 1 ]
  run git show --name-only --format= HEAD
  [[ "$output" =~ ".update-locks/steps/defer-noop" ]]
}

@test "ul_run_step other non-zero is a full rollback (no stamp) and a failure" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  before=$(git rev-parse HEAD)
  hard_fail() { echo "mess" > file.txt; return 1; }
  ul_run_step "hard-fail" "msg" hard_fail

  [ "$(git rev-parse HEAD)" = "$before" ]        # no commit at all
  [ ! -f "$TEST_DIR/.update-locks/steps/hard-fail" ]  # no stamp
  [ "$_UL_STEPS_FAILED" -eq 1 ]
  git diff --quiet
}
```

- [ ] **Step 2: Run; verify failures**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-locks-lib.bats`
Expected: the new/changed tests FAIL (`ul_run_step` doesn't yet write stamps, handle `75`, or commit on no-op).

- [ ] **Step 3: Rewrite `ul_run_step` and add the two commit helpers**

In `lib/scripts/update-locks-lib.bash`, replace the success/failure block of `ul_run_step` (lines 217-244, from `if [[ $rc -eq 0 ]]; then` through the closing `fi` of the else) with:

```bash
  if [[ $rc -eq 0 ]]; then
    if _ul_commit_updated "$step_name" "$commit_msg"; then
      _UL_STEPS_SUCCEEDED=$((_UL_STEPS_SUCCEEDED + 1))
    fi
  elif [[ $rc -eq $UL_RC_ATTEMPTED ]]; then
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    if _ul_commit_stamp_only "$step_name"; then
      echo "  ⊘ Step '${step_name}' attempted — no update applied (deferred)"
      _UL_STEPS_DEFERRED=$((_UL_STEPS_DEFERRED + 1))
    fi
  else
    echo "  ✗ Step '${step_name}' failed (exit code ${rc})"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
  fi
}
```

Then, immediately after `ul_run_step`'s closing brace, add the two helpers:

```bash
# Commit a successful step: format content if any changed, write the stamp,
# and commit everything in one commit (content + stamp, or stamp-only on a
# no-op success). On fmt/commit failure: roll back, record failure, return 1.
_ul_commit_updated() {
  local step_name="$1" commit_msg="$2"
  if ! git diff --quiet || ! git diff --cached --quiet; then
    if ! nix fmt; then
      echo "  ✗ Step '${step_name}' nix fmt failed"
      git reset --hard HEAD 2>/dev/null || true
      git clean -fd 2>/dev/null || true
      _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
      _UL_FAILED_STEPS+=("$step_name")
      return 1
    fi
  fi
  ul_write_stamp "$step_name"
  if ! git add -A || ! git commit -m "$commit_msg" >/dev/null; then
    echo "  ✗ Step '${step_name}' commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}

# Commit only the step's stamp (used after a deferral rolled back content).
_ul_commit_stamp_only() {
  local step_name="$1"
  ul_write_stamp "$step_name"
  if ! git add -- "$_UL_STAMP_DIR/$step_name" || \
     ! git commit -m "update-locks: ${step_name} attempted, no update applied" >/dev/null; then
    echo "  ✗ Step '${step_name}' stamp commit failed"
    git reset --hard HEAD 2>/dev/null || true
    git clean -fd 2>/dev/null || true
    _UL_STEPS_FAILED=$((_UL_STEPS_FAILED + 1))
    _UL_FAILED_STEPS+=("$step_name")
    return 1
  fi
  return 0
}
```

- [ ] **Step 4: Run; verify the new tests pass and the existing ones still pass**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-locks-lib.bats`
Expected: PASS for the deferral/stamp tests and the previously-passing success/failure/signal tests.

- [ ] **Step 5: Commit**

```bash
git add lib/scripts/update-locks-lib.bash lib/tests/test-update-locks-lib.bats
git commit -m "feat: three-outcome ul_run_step (0=commit, 75=defer+stamp, other=rollback)"
```

### Task 7: `ul_finalize` reports Deferred

**Files:**

- Modify: `lib/scripts/update-locks-lib.bash` (`ul_finalize`)
- Test: `lib/tests/test-update-locks-lib.bats`

- [ ] **Step 1: Add a failing test for the Deferred line**

Add to `lib/tests/test-update-locks-lib.bats` after the existing `ul_finalize` tests:

```bash
@test "ul_finalize reports a Deferred count and exits 0 when only deferrals" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  defer() { ul_attempted; }
  ul_run_step "d1" "msg" defer

  run ul_finalize
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Deferred: 1" ]]
  [[ "$output" =~ "successfully" ]]
}
```

- [ ] **Step 2: Run; verify it fails**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-locks-lib.bats`
Expected: FAIL — no "Deferred:" line in the summary.

- [ ] **Step 3: Add the Deferred line to `ul_finalize`**

In `ul_finalize` (lines 247-253), after the `Passed:` line add a `Deferred:` line, leaving the existing `Ran:`/`Passed:`/`Failed:`/`Skipped:` text spacing untouched (so existing assertions still match):

```bash
  echo "=== Update Summary ==="
  echo "  Ran:     ${_UL_STEPS_RAN}"
  echo "  Passed:  ${_UL_STEPS_SUCCEEDED}"
  echo "  Deferred: ${_UL_STEPS_DEFERRED}"
  echo "  Failed:  ${_UL_STEPS_FAILED}"
  echo "  Skipped: ${_UL_STEPS_SKIPPED}"
```

(Exit logic unchanged: `ul_finalize` exits 1 only when `_UL_STEPS_FAILED > 0`.)

- [ ] **Step 4: Run; verify pass**

Run: `UL_LIB_SCRIPTS_DIR="$PWD/lib/scripts" nix run nixpkgs#bats -- lib/tests/test-update-locks-lib.bats`
Expected: PASS, including the existing `"ul_finalize reports correct counts"` test (its `Ran:     2` / `Passed:  1` / `Failed:  1` substrings are unchanged).

- [ ] **Step 5: Commit**

```bash
git add lib/scripts/update-locks-lib.bash lib/tests/test-update-locks-lib.bats
git commit -m "feat: ul_finalize reports Deferred count"
```

---

## Phase 4 — Integration verification

### Task 8: Full check suite + real-repo smoke test

**Files:** none (verification only)

- [ ] **Step 1: Run the authoritative sandboxed lib check**

Run: `nix build .#checks.aarch64-darwin.test-update-locks-lib -L`
Expected: build succeeds (both bats files pass in the sandbox).

- [ ] **Step 2: Run the repo's full flake check**

Run: `nix flake check -L`
Expected: passes (Go tests, shellcheck, bats, formatting). If shellcheck flags the new bash, fix inline and re-run.

- [ ] **Step 3: Smoke-test a real `update-locks.sh` produces an in-repo stamp commit**

In this repo (single `nix flake update` step), on a throwaway branch, force a run and confirm a stamp file is committed:

```bash
git switch -c tmp/stamp-smoke
NIX_UL_FORCE_UPDATE=true WORKSPACE_ROOT="$PWD/.." ./update-locks.sh
git log -1 --name-only --format='%s'
```

Expected: the last commit's files include `.update-locks/steps/nix-flake-update` (plus `flake.lock` if inputs changed). Then clean up:

```bash
git switch -  && git branch -D tmp/stamp-smoke
```

- [ ] **Step 4: Confirm `nvd` renders on a real apply (manual, user-run)**

`nvd` only renders when the system profile actually changes, and apply uses `sudo`. Ask the user to run, in the terminal repo, after this lands and a real change exists:

```bash
pn workspace apply
```

Expected: a `--== Package changes ==--` block followed by `nvd diff` output. (If no profile change, the block is correctly absent.)

- [ ] **Step 5: Final commit (if any fixups were needed in Steps 1-2)**

```bash
git add -A
git commit -m "test: pass full flake check for in-repo update-locks gate"
```

---

## Self-Review

**Spec coverage:**

- Part 1 `nvd` wrap (`--suffix`, drop `propagatedBuildInputs`, comment) → Task 1. ✓
- In-repo per-step plain-text stamps → Tasks 2-3. ✓
- `ul_should_run` value-based + no CI bypass + fail-open → Task 3. ✓
- `0`/`75`/other three-outcome `ul_run_step`, stamp-only on no-op success and on deferral → Task 6. ✓
- `UL_RC_ATTEMPTED`/`ul_attempted` → Task 5. ✓
- `ul_finalize` Deferred counter → Task 7. ✓
- Remove vestigial `ul_mark_done`/`ul_needs_rebuild`/`ul_mark_applied` + tests → Task 4. ✓
- `pn`'s overall exit unaffected (`75` never escapes a step) → covered: `ul_finalize` exits 1 only on `_UL_STEPS_FAILED` (Task 7) and deferral increments only `_UL_STEPS_DEFERRED` (Task 6 test). ✓
- **Deferred to follow-up plan (explicitly out of scope here):** the 11 per-step `75` wirings (Go/npm `update-deps.sh`, uv verify-build, `goccc`/`toktrack` restructure, GH-bumper asset-list check) and any `update-flakes.yml` adjustment. The spec's CI section needs no YAML change (CI just respects the now-committed gate), so nothing here.

**Placeholder scan:** No TBD/TODO. The one manual step (Task 8 Step 4, `nvd` on real apply) is inherently user/hardware-dependent (needs `sudo` + a real profile change) and is marked as user-run, not a placeholder.

**Type/name consistency:** `_UL_STAMP_DIR` (set in `ul_init`, read in `ul_should_run`/`ul_write_stamp`/`_ul_commit_stamp_only`), `ul_write_stamp`, `_ul_iso_to_epoch`, `UL_RC_ATTEMPTED`, `ul_attempted`, `_UL_STEPS_DEFERRED`, `_ul_commit_updated`, `_ul_commit_stamp_only` — used consistently across Tasks 2/3/5/6/7. `ul_init` two-arg signature updated at its only caller (`ul_setup`, Task 5 Step 2).
