# pn-workspace No-Upstream Handling — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pn-workspace-update`, `pn-workspace-push`, and `pn-workspace-rebase` skip remote-dependent git operations gracefully when a project has no upstream (instead of failing).

**Architecture:** Add a single shared helper `workspace_has_upstream` in `pn-lib.bash` that returns 0 if the current repo has both a remote configured and the current branch has a tracking branch. Each of the three scripts uses the helper to gate remote-dependent steps; when the helper returns non-zero, the script prints an informational message containing the project name and current branch (or `DETACHED HEAD`) and continues. `pn-workspace-update` still runs `update-locks.sh` even when upstream is missing.

**Tech Stack:** bash, bats (test framework), nix flake, jq, yq.

**Spec:** `docs/superpowers/specs/2026-04-29-pn-workspace-no-upstream-design.md`

---

## File Structure

| File                                                                 | Action | Responsibility                                                                                                                                      |
| -------------------------------------------------------------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `modules/pn/pn-lib/pn-lib.bash`                                      | modify | Add `workspace_has_upstream` helper                                                                                                                 |
| `modules/pn/pn-lib/tests/test-pn-lib.bats`                           | modify | Unit tests for `workspace_has_upstream`                                                                                                             |
| `modules/pn/test-support/test_helper.bash`                           | modify | Extend `create_mock_git` to support `MOCK_GIT_NO_REMOTE` and `MOCK_GIT_NO_UPSTREAM` env-var toggles, plus `MOCK_GIT_BRANCH` for current branch name |
| `modules/pn/pn-workspace-update/pn-workspace-update.sh`              | modify | Gate `git pull` and `git push` on `workspace_has_upstream`; print skip message                                                                      |
| `modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats` | modify | Add no-upstream skip case                                                                                                                           |
| `modules/pn/pn-workspace-push/pn-workspace-push.sh`                  | modify | Gate `git push` on `workspace_has_upstream`; print skip message                                                                                     |
| `modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats`     | modify | Add no-upstream skip case + detached-HEAD case                                                                                                      |
| `modules/pn/pn-workspace-rebase/pn-workspace-rebase.sh`              | modify | Gate `git mu` on `workspace_has_upstream`; print skip message                                                                                       |
| `modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats` | modify | Add no-upstream skip case                                                                                                                           |

No new files. No deletions.

---

## Conventions used by these tests

The bats tests source `pn-lib.bash` and the script under test in a single `bash -c` invocation. The `PATH` is prepended with `$TEST_DIR` so mock binaries (`git`, `pn-discover-workspace`, etc.) shadow real ones. The existing `create_mock_git` mock in `modules/pn/test-support/test_helper.bash` echoes `"Mock: git $*"` for unrecognized invocations; this plan extends it to honour env-var toggles for testing the no-upstream path.

---

## Task 1: Add `workspace_has_upstream` helper to `pn-lib.bash`

**Files:**

- Modify: `modules/pn/pn-lib/pn-lib.bash` (append within the `# ─── Workspace functions ───` section)
- Test: `modules/pn/pn-lib/tests/test-pn-lib.bats` (append at end of file)

- [ ] **Step 1.1: Write failing unit tests for `workspace_has_upstream`**

Append to `modules/pn/pn-lib/tests/test-pn-lib.bats`:

```bash
# ─── workspace_has_upstream ───────────────────────────────────────────────────

@test "workspace_has_upstream returns 1 when no remotes configured" {
  local repo="$TEST_DIR/repo-no-remote"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}

@test "workspace_has_upstream returns 1 when remote exists but no tracking branch" {
  local repo="$TEST_DIR/repo-no-upstream"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  git -C "$repo" remote add origin /nonexistent
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}

@test "workspace_has_upstream returns 0 when remote and tracking branch both present" {
  local upstream="$TEST_DIR/repo-upstream.git"
  local repo="$TEST_DIR/repo-tracked"
  git init -q --bare "$upstream"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m init
  git -C "$repo" remote add origin "$upstream"
  git -C "$repo" push -q -u origin main
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 0 ]
}

@test "workspace_has_upstream returns 1 in detached HEAD state" {
  local repo="$TEST_DIR/repo-detached"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" -c user.email=t@t -c user.name=T commit -q --allow-empty -m c1
  local sha
  sha=$(git -C "$repo" rev-parse HEAD)
  git -C "$repo" checkout -q "$sha"
  cd "$repo"
  run workspace_has_upstream
  [ "$status" -eq 1 ]
}
```

- [ ] **Step 1.2: Run the new tests; verify they fail**

Run: `bats modules/pn/pn-lib/tests/test-pn-lib.bats -f workspace_has_upstream`

Expected: 4 tests fail with `command not found: workspace_has_upstream` or `workspace_has_upstream: command not found`.

- [ ] **Step 1.3: Implement `workspace_has_upstream` in `pn-lib.bash`**

Append immediately after the existing `workspace_read_toml` function in `modules/pn/pn-lib/pn-lib.bash`:

```bash
# Returns 0 if the current repo HEAD has a usable upstream (remote exists AND
# current branch has tracking branch configured). Returns 1 otherwise.
# Does NOT fetch — purely a local config check. Run from inside the repo
# working tree.
workspace_has_upstream() {
  [[ -n $(git remote 2>/dev/null) ]] || return 1
  git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1
}
```

- [ ] **Step 1.4: Run the unit tests; verify they pass**

Run: `bats modules/pn/pn-lib/tests/test-pn-lib.bats -f workspace_has_upstream`

Expected: 4 tests pass.

- [ ] **Step 1.5: Run full pn-lib test suite to confirm no regressions**

Run: `bats modules/pn/pn-lib/tests/test-pn-lib.bats`

Expected: all tests pass.

- [ ] **Step 1.6: Commit**

```bash
git add modules/pn/pn-lib/pn-lib.bash modules/pn/pn-lib/tests/test-pn-lib.bats
git commit -m "feat(pn-lib): add workspace_has_upstream helper"
```

---

## Task 2: Extend `create_mock_git` in test_helper to support no-upstream toggles

The script-level bats tests use the mock `git` from `test-support/test_helper.bash`. The current mock echoes `"Mock: git $*"` for everything except `git remote get-url origin`. To exercise the no-upstream skip path, the mock must:

1. Print nothing (and exit 0) for `git remote` when `MOCK_GIT_NO_REMOTE=1`.
2. Exit 1 for `git rev-parse --abbrev-ref --symbolic-full-name @{u}` when `MOCK_GIT_NO_UPSTREAM=1` or `MOCK_GIT_NO_REMOTE=1`.
3. Print `${MOCK_GIT_BRANCH:-main}` for `git branch --show-current` (so tests can assert the branch name in skip messages, including empty string for detached HEAD).
4. Continue current behaviour for everything else (echo `Mock: git $*`, exit 0). In particular, plain `git remote` (no toggle) must still print non-empty so the happy-path tests continue to detect the remote — emit `origin` to match a typical configured remote.

**Files:**

- Modify: `modules/pn/test-support/test_helper.bash`

- [ ] **Step 2.1: Update `create_mock_git` to honour env-var toggles**

In `modules/pn/test-support/test_helper.bash`, replace the entire `create_mock_git` function with:

```bash
# Create mock git command.
# Handles a few subcommands specially:
#   git remote get-url origin      → echoes $MOCK_GIT_REMOTE_URL
#                                    (default: https://github.com/example/repo.git)
#   git remote                     → empty when MOCK_GIT_NO_REMOTE=1, else "origin"
#   git rev-parse --abbrev-ref --symbolic-full-name @{u}
#                                  → exits 1 when MOCK_GIT_NO_REMOTE=1
#                                    or MOCK_GIT_NO_UPSTREAM=1, else echoes
#                                    "origin/${MOCK_GIT_BRANCH:-main}" and exits 0
#   git branch --show-current      → echoes ${MOCK_GIT_BRANCH-main}
#                                    (empty string allowed for detached HEAD)
# All other invocations echo "Mock: git <args>" and exit 0.
create_mock_git() {
  cat >"$TEST_DIR/git" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "remote" && "$2" == "get-url" && "$3" == "origin" ]]; then
  echo "${MOCK_GIT_REMOTE_URL:-https://github.com/example/repo.git}"
  exit 0
fi
if [[ "$1" == "remote" && $# -eq 1 ]]; then
  if [[ -n "${MOCK_GIT_NO_REMOTE:-}" ]]; then
    exit 0
  fi
  echo "origin"
  exit 0
fi
if [[ "$1" == "rev-parse" && "$2" == "--abbrev-ref" && "$3" == "--symbolic-full-name" && "$4" == "@{u}" ]]; then
  if [[ -n "${MOCK_GIT_NO_REMOTE:-}" || -n "${MOCK_GIT_NO_UPSTREAM:-}" ]]; then
    exit 1
  fi
  echo "origin/${MOCK_GIT_BRANCH:-main}"
  exit 0
fi
if [[ "$1" == "branch" && "$2" == "--show-current" ]]; then
  printf '%s\n' "${MOCK_GIT_BRANCH-main}"
  exit 0
fi
echo "Mock: git $*"
exit 0
EOF
  chmod +x "$TEST_DIR/git"
  export PATH="$TEST_DIR:$PATH"
}
```

- [ ] **Step 2.2: Run the existing happy-path script tests to confirm no regressions**

Run from repo root:

```bash
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
bats modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats
bats modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats
```

Expected: all existing tests still pass. (No script changes yet, but the mock now returns `origin` for `git remote` and a tracking branch for `git rev-parse @{u}`, so future helper calls under the happy path will succeed.)

- [ ] **Step 2.3: Commit**

```bash
git add modules/pn/test-support/test_helper.bash
git commit -m "test(pn): extend mock git with no-upstream toggles"
```

---

## Task 3: Gate `git pull` and `git push` in `pn-workspace-update.sh`

**Files:**

- Modify: `modules/pn/pn-workspace-update/pn-workspace-update.sh` (per-project loop body, lines ~106-128)
- Test: `modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats`

- [ ] **Step 3.1: Write failing tests for the no-upstream skip path**

Append to `modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats` (immediately before the final closing of the file, after the `unknown override-path key errors` test):

```bash
@test "pn-workspace-update skips pull/push when project has no remote" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_REMOTE=1
      export MOCK_GIT_BRANCH=feature-branch
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping pull/push for repo-base"
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping pull/push for terminal-flake"
    echo "$output" | grep -q "update-locks.sh ran"
    ! echo "$output" | grep -q "Mock: git pull"
    ! echo "$output" | grep -q "Mock: git push"
}

@test "pn-workspace-update skips pull/push when branch has no tracking branch" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=local-only
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping pull/push"
    echo "$output" | grep -q "update-locks.sh ran"
    ! echo "$output" | grep -q "Mock: git pull"
    ! echo "$output" | grep -q "Mock: git push"
}
```

- [ ] **Step 3.2: Run the new tests; verify they fail**

Run: `bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats -f "no remote\|no tracking"`

Expected: both tests fail. The first failure surfaces because the unmodified script still calls `git pull` (which under `MOCK_GIT_NO_REMOTE=1` still echoes `Mock: git pull` because that branch of the mock isn't toggled). The grep for `"no upstream for branch"` will fail because the message doesn't exist yet.

- [ ] **Step 3.3: Modify the per-project loop to gate pull/push**

In `modules/pn/pn-workspace-update/pn-workspace-update.sh`, replace the entire `while IFS= read -r project_path; do ... done` block (currently lines ~106-128) with:

```bash
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || exit 1

  if workspace_has_upstream; then
    git pull --rebase --autostash &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  fi

  ./update-locks.sh &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  if workspace_has_upstream; then
    git push &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  else
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
  fi

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
```

- [ ] **Step 3.4: Update help text**

In `modules/pn/pn-workspace-update/pn-workspace-update.sh`, find the `cat <<'HELP'` block. Append this paragraph after the existing Purpose paragraph (before `Usage:`):

```
Projects without a configured upstream are still refreshed locally
(update-locks.sh runs); pull and push are skipped with an informational
message.
```

The Purpose section becomes (in full):

```
Purpose: Updates all flake dependencies (lock files) for every workspace repo
without applying changes. For each project in order, it pulls the latest remote
changes, runs update-locks.sh, and pushes. After all projects are updated,
regenerates the pn-workspace.lock file.

Projects without a configured upstream are still refreshed locally
(update-locks.sh runs); pull and push are skipped with an informational
message.
```

- [ ] **Step 3.5: Run the new tests; verify they pass**

Run: `bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats -f "no remote\|no tracking"`

Expected: both tests pass.

- [ ] **Step 3.6: Run the full pn-workspace-update test suite**

Run: `bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats`

Expected: all tests pass.

- [ ] **Step 3.7: Commit**

```bash
git add modules/pn/pn-workspace-update/pn-workspace-update.sh modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
git commit -m "feat(pn-workspace-update): skip pull/push when no upstream"
```

---

## Task 4: Gate `git push` in `pn-workspace-push.sh`

**Files:**

- Modify: `modules/pn/pn-workspace-push/pn-workspace-push.sh` (per-project loop body, lines ~83-88)
- Test: `modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats`

- [ ] **Step 4.1: Write failing tests for the no-upstream skip path**

Append to `modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats`:

```bash
@test "pn-workspace-push skips push when project has no remote" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_REMOTE=1
      export MOCK_GIT_BRANCH=feature-branch
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping push for repo-base"
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping push for terminal-flake"
    ! echo "$output" | grep -q "Mock: git push"
}

@test "pn-workspace-push skips push when branch has no tracking branch" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=local-only
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping push"
    ! echo "$output" | grep -q "Mock: git push"
}

@test "pn-workspace-push reports DETACHED HEAD when current branch is empty" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'DETACHED HEAD' — skipping push"
}
```

- [ ] **Step 4.2: Run the new tests; verify they fail**

Run: `bats modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats -f "no remote\|no tracking\|DETACHED HEAD"`

Expected: 3 tests fail; messages do not yet exist.

- [ ] **Step 4.3: Modify the per-project loop to gate push**

In `modules/pn/pn-workspace-push/pn-workspace-push.sh`, replace the entire final `while IFS= read -r project_path; do ... done` block (currently lines ~83-88) with:

```bash
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Push $project_name ==--  "
  cd "$project_path" || exit 1
  if workspace_has_upstream; then
    git push
  else
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping push for $project_name"
  fi
done < <(echo "$workspace_json" | jq -r '.[] | .path')
```

- [ ] **Step 4.4: Update help text**

In the `cat <<'HELP'` block, append after the existing Purpose paragraph (before `Usage:`):

```
Projects without a configured upstream are skipped with an informational
message.
```

- [ ] **Step 4.5: Run the new tests; verify they pass**

Run: `bats modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats -f "no remote\|no tracking\|DETACHED HEAD"`

Expected: 3 tests pass.

- [ ] **Step 4.6: Run the full pn-workspace-push test suite**

Run: `bats modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats`

Expected: all tests pass.

- [ ] **Step 4.7: Commit**

```bash
git add modules/pn/pn-workspace-push/pn-workspace-push.sh modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats
git commit -m "feat(pn-workspace-push): skip push when no upstream"
```

---

## Task 5: Gate `git mu` in `pn-workspace-rebase.sh`

**Files:**

- Modify: `modules/pn/pn-workspace-rebase/pn-workspace-rebase.sh` (per-project loop body, lines ~83-88)
- Test: `modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats`

- [ ] **Step 5.1: Write failing tests for the no-upstream skip path**

Append to `modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats`:

```bash
@test "pn-workspace-rebase skips rebase when project has no remote" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_REMOTE=1
      export MOCK_GIT_BRANCH=feature-branch
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping rebase for repo-base"
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping rebase for terminal-flake"
    ! echo "$output" | grep -q "Mock: git mu"
}

@test "pn-workspace-rebase skips rebase when branch has no tracking branch" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=local-only
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping rebase"
    ! echo "$output" | grep -q "Mock: git mu"
}
```

- [ ] **Step 5.2: Run the new tests; verify they fail**

Run: `bats modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats -f "no remote\|no tracking"`

Expected: 2 tests fail; messages do not yet exist.

- [ ] **Step 5.3: Modify the per-project loop to gate rebase**

In `modules/pn/pn-workspace-rebase/pn-workspace-rebase.sh`, replace the entire final `while IFS= read -r project_path; do ... done` block (currently lines ~83-88) with:

```bash
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Rebase $project_name ==--  "
  cd "$project_path" || exit 1
  if workspace_has_upstream; then
    git mu
  else
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping rebase for $project_name"
  fi
done < <(echo "$workspace_json" | jq -r '.[] | .path')
```

- [ ] **Step 5.4: Update help text**

In the `cat <<'HELP'` block, append after the existing Purpose paragraph (before `Usage:`):

```
Projects without a configured upstream are skipped with an informational
message.
```

- [ ] **Step 5.5: Run the new tests; verify they pass**

Run: `bats modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats -f "no remote\|no tracking"`

Expected: 2 tests pass.

- [ ] **Step 5.6: Run the full pn-workspace-rebase test suite**

Run: `bats modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats`

Expected: all tests pass.

- [ ] **Step 5.7: Commit**

```bash
git add modules/pn/pn-workspace-rebase/pn-workspace-rebase.sh modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats
git commit -m "feat(pn-workspace-rebase): skip rebase when no upstream"
```

---

## Task 6: Repo-wide validation

The user's global rules require these checks to pass before claiming completion:

- `pre-commit run --all-files` if `.pre-commit-config.yaml` exists.
- `nix flake check && darwin-rebuild check --flake .` if `flake.nix` exists.

**Files:** none modified in this task — this is a verification gate.

- [ ] **Step 6.1: Run pre-commit if the project uses it**

```bash
if [[ -f .pre-commit-config.yaml ]]; then
  pre-commit run --all-files
fi
```

Expected: exit 0. If anything fails, fix the offending file, re-stage, and create a NEW commit (do not amend).

- [ ] **Step 6.2: Run `nix flake check`**

```bash
nix flake check
```

Expected: exit 0. This runs all bats tests packaged by the flake plus any other checks.

- [ ] **Step 6.3: Run `darwin-rebuild check --flake .`**

```bash
darwin-rebuild check --flake .
```

Expected: exit 0.

- [ ] **Step 6.4: Run all four bats files directly as a final spot check**

```bash
bats modules/pn/pn-lib/tests/test-pn-lib.bats
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
bats modules/pn/pn-workspace-push/tests/test-pn-workspace-push.bats
bats modules/pn/pn-workspace-rebase/tests/test-pn-workspace-rebase.bats
```

Expected: all tests pass in all four files.

- [ ] **Step 6.5: No commit needed for this task** — validation only. If any fix was required during validation, that fix should already be committed via a fix commit at the time it was made.

---

## Notes for the implementer

- The existing `pn-workspace-update.sh` uses `_child_pid` and a SIGTERM handler to interrupt long-running git operations. The new gate keeps that behaviour: `git pull` and `git push` are still invoked under `&` + `wait` when upstream is present. The `update-locks.sh` invocation is unchanged.
- `pn-workspace-upgrade.sh` is unchanged because it shells out to `pn-workspace-update`; the fix flows through automatically.
- All three scripts source `pn-lib.bash` indirectly via the test runner (`source '${LIB_PATH%%:*}'` in tests) and via the nix-built wrapper at runtime. `workspace_has_upstream` is therefore in scope wherever `workspace_resolve_root` is.
- If `nix flake check` reports a shellcheck violation in the new code, address it inline (do not bypass with `--no-verify`).
