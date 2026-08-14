#!/usr/bin/env bats
# shellcheck disable=SC1090

if [[ -n ${UL_LIB_SCRIPTS_DIR:-} ]]; then
  UL_LOCKS_LIB="$UL_LIB_SCRIPTS_DIR/update-locks-lib.bash"
else
  UL_LOCKS_LIB="$(cd "$BATS_TEST_DIRNAME/../scripts" && pwd)/update-locks-lib.bash"
fi

# Replace the shebang on $1 with one that uses an absolute bash path.
# Required for environments where /usr/bin/env doesn't exist (e.g. the
# Nix build sandbox, where only /nix/store paths are visible).
# Uses a temp file rather than `sed -i` so it works under both GNU and
# BSD/macOS sed (BSD `sed -i` requires a backup-suffix argument), keeping
# `bats lib/tests` a usable fast local loop on macOS (bead pg2-uepg7).
_fix_mock_shebang() {
  local f="$1" tmp
  tmp=$(mktemp)
  {
    printf '#!%s\n' "$(command -v bash)"
    tail -n +2 "$f"
  } >"$tmp"
  cat "$tmp" >"$f"
  rm -f "$tmp"
}

setup() {
  TEST_DIR=$(mktemp -d)
  # XDG_STATE_HOME must live OUTSIDE the repo: _ul_ensure_pre_commit_hooks writes
  # a pre-commit-drv-path marker under it during ul_setup. If it were nested in
  # TEST_DIR (the git repo), `git add -A` in a step's commit would sweep that
  # marker into the commit, polluting the per-step stamp commits the tests assert.
  STATE_DIR=$(mktemp -d)
  export XDG_STATE_HOME="$STATE_DIR"
  export NIX_UL_FORCE_UPDATE="true"

  # Mock nix so that `nix fmt` is a no-op in tests
  # (real nix fmt requires treefmt/flake context not available in test sandbox)
  # Mock lives OUTSIDE TEST_DIR to survive `git clean -fd` inside test steps
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  export PATH="$MOCK_BIN:$PATH"

  # HERMETIC GIT (bead pg2-klyn6, mirroring the pg2-39rz2 Go fix's TestMain in
  # modules/pn/internal/workspace/realgit_test.go): neutralise the developer's
  # GLOBAL and SYSTEM git config for every git invocation in this test — the
  # harness's own, the library under test's, and any `bash -c` / background
  # subprocess a test spawns, all of which inherit these exports.
  #
  # Setting only repo-LOCAL user.email/user.name below is not isolation: every
  # other key still merges in from ~/.gitconfig, $XDG_CONFIG_HOME/git/config and
  # /etc/gitconfig, so the suite's outcome depended on whose machine ran it. The
  # concrete hazard is `core.fsmonitor=true`: it would be inherited by every temp
  # repo these tests create, and then ul_setup's clean-tree gate refreshes the
  # index and spawns git's native fsmonitor daemon — which is deterministically
  # wedged on some setups (bead pg2-mgcv5), hanging the whole suite.
  #
  # /dev/null is the NEUTRAL setting. A test that deliberately needs a global
  # value opts in by pointing GIT_CONFIG_GLOBAL at a temp file of its own (see
  # the fsmonitor scoping tests below); it must never touch the real one.
  # Requires git >= 2.32 for these two variables; this repo pins a modern git.
  export GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_SYSTEM=/dev/null

  cd "$TEST_DIR" || return 1
  git init
  git config user.email "test@test.com"
  git config user.name "Test"
  echo "initial" > file.txt
  git add file.txt
  git commit -m "initial"
}

teardown() {
  cd /
  rm -rf "$TEST_DIR"
  rm -rf "${MOCK_BIN:-}"
  rm -rf "${STATE_DIR:-}"
}

# --- ul_setup ---

@test "ul_setup succeeds on clean workspace" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  [ "$_UL_STEPS_RAN" -eq 0 ]
  [ "$_UL_STEPS_SUCCEEDED" -eq 0 ]
  [ "$_UL_STEPS_FAILED" -eq 0 ]
  [ "$_UL_STEPS_SKIPPED" -eq 0 ]
}

@test "ul_setup exits 1 on dirty workspace" {
  echo "dirty" > file.txt
  run bash -c "source '$UL_LOCKS_LIB'; ul_setup test-project '$TEST_DIR'"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not clean" ]]
}

@test "ul_setup exits 1 on staged changes" {
  echo "staged" > file.txt
  git add file.txt
  run bash -c "source '$UL_LOCKS_LIB'; ul_setup test-project '$TEST_DIR'"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not clean" ]]
}

@test "ul_setup regenerates the gitignored .pre-commit-config.yaml without committing it and passes the gate" {
  # Post ADR 0016 the generated config is gitignored, never tracked. Simulate the
  # git-hooks.nix shellHook regenerating it on dev-shell entry: because it is
  # ignored, that must NOT dirty the tracked tree, must NOT be committed, and
  # ul_setup must pass the clean-tree gate.
  echo ".pre-commit-config.yaml" > .gitignore
  git add .gitignore
  git commit -m "gitignore generated pre-commit config"

  # nix mock: tier-1 `build … --print-out-paths` prints a drv path; `run
  # .#install-pre-commit-hooks` (re)generates the ignored config; all else
  # (eval/fmt) is a silent no-op.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *build*install-pre-commit-hooks*) echo "/nix/store/deadbeef-install-pre-commit-hooks" ;;
  *run*install-pre-commit-hooks*) echo "generated-config" > .pre-commit-config.yaml ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  local before_hash
  before_hash=$(git rev-parse HEAD)

  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR" # must NOT exit 1

  # The config was (re)generated but stays untracked + ignored.
  [ "$(cat .pre-commit-config.yaml)" = "generated-config" ]
  git diff --quiet          # tracked working tree clean
  git diff --cached --quiet # nothing staged
  # No pre-commit commit was made — HEAD is unchanged.
  [ "$(git rev-parse HEAD)" = "$before_hash" ]
}

@test "ul_setup still exits 1 when a non-managed file is dirty alongside the pre-commit config" {
  # Regenerating the gitignored .pre-commit-config.yaml must not mask a genuine
  # uncommitted edit to a tracked file: the gate must still fire, and the edit
  # must not be destroyed on the gate-fail path.
  echo ".pre-commit-config.yaml" > .gitignore
  git add .gitignore
  git commit -m "gitignore generated pre-commit config"
  echo "user edit" > file.txt # genuine uncommitted work

  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *build*install-pre-commit-hooks*) echo "/nix/store/deadbeef-install-pre-commit-hooks" ;;
  *run*install-pre-commit-hooks*) echo "generated-config" > .pre-commit-config.yaml ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  run bash -c "source '$UL_LOCKS_LIB'; ul_setup test-project '$TEST_DIR'"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not clean" ]]
  # the user's edit survived (no destructive cleanup on the gate-fail path)
  [ "$(cat file.txt)" = "user edit" ]
}

@test "ul_setup exits 1 on untracked file and does NOT delete it" {
  echo "precious user data" > untracked.txt # never git-added
  run bash -c "source '$UL_LOCKS_LIB'; ul_setup test-project '$TEST_DIR'"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not clean" ]]
  # The pre-existing untracked file MUST survive the gate-fail path — at exit 1
  # the trap is still the non-destructive _ul_restore_fsmonitor (the full
  # cleanup trap is armed only AFTER the gate).
  [ -f "$TEST_DIR/untracked.txt" ]
  [ "$(cat "$TEST_DIR/untracked.txt")" = "precious user data" ]
}

# --- _ul_ensure_pre_commit_hooks: hooks-dir resolution (bead pg2-rltuo) ---
#
# Tier 2 ("is the hook binary still valid — not GC'd?") must first LOCATE the
# installed hook. Two REAL configurations defeated it, both because the resolved
# core.hooksPath was JOINED onto the repo dir:
#
#   (a) an ABSOLUTE core.hooksPath — what every clone in this workspace holds —
#       produced "<repo>//<repo>/.git/hooks/pre-commit", which never exists;
#   (b) with core.hooksPath UNSET the ".git/hooks" fallback is RELATIVE, so it
#       cannot name a LINKED WORKTREE's hooks dir: a worktree's .git is a FILE
#       and the hooks live in the COMMON dir under the main repo.
#
# Either miss left needs_install=true on EVERY run, so the GC check below the
# lookup was unreachable. Both tests therefore assert the hook is FOUND — no
# "hook not found" message and no reinstall at all — not merely that the path
# string looks plausible.

# Seed the scaffolding every Tier-2 test shares: a nix mock whose
# `run .#install-pre-commit-hooks` is OBSERVABLE via a marker file, and a
# current Tier-3 drv-path marker so any reinstall observed can only have come
# from Tier 2. The caller owns the hook file itself.
_seed_pre_commit_hook_check_env() {
  local drv="/nix/store/deadbeef-install-pre-commit-hooks"

  UL_TEST_REINSTALL_MARKER="$STATE_DIR/reinstall-ran"
  export UL_TEST_REINSTALL_MARKER
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *build*install-pre-commit-hooks*) echo "/nix/store/deadbeef-install-pre-commit-hooks" ;;
  *run*install-pre-commit-hooks*) : > "$UL_TEST_REINSTALL_MARKER" ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  # Tier 3 agrees the derivation is unchanged, so any reinstall observed came
  # from Tier 2. Mirrors what ul_init/ul_setup would set.
  # shellcheck disable=SC2034  # both are read by the sourced update-locks-lib
  UL_STATE_DIR="$STATE_DIR/update-locks"
  # shellcheck disable=SC2034  # ditto
  _UL_PROJECT="test-project"
  mkdir -p "$UL_STATE_DIR/$_UL_PROJECT"
  echo "$drv" > "$UL_STATE_DIR/$_UL_PROJECT/pre-commit-drv-path"
}

# A real, NON-store executable a hook can exec. It lives OUTSIDE any working
# tree so `git clean -fd` in a test step cannot remove it.
_seed_hook_runner_mock() {
  cat > "$MOCK_BIN/hook-runner" <<'RUNNER'
#!/usr/bin/env bash
exit 0
RUNNER
  _fix_mock_shebang "$MOCK_BIN/hook-runner"
  chmod +x "$MOCK_BIN/hook-runner"
}

# Print a /nix/store path that DEFINITELY exists, derived rather than
# hardcoded. Tier 2's GC check reads only EXISTENCE, so which entry it is does
# not matter — it just has to be a live one, and it cannot be fabricated under
# $TEST_DIR because the check keys on the literal /nix/store prefix.
_existing_store_path() {
  local p
  # Cheapest: the store entry owning this run's bash. True inside the nix check
  # (its whole PATH is store paths); not true of a profile-symlink bash locally.
  for p in "${BASH:-}" "$(command -v bash || true)"; do
    if [[ $p == /nix/store/?* && -e $p ]]; then
      printf '%s\n' "$p"
      return 0
    fi
  done
  # Local fast loop: any live store entry will do.
  for p in /nix/store/*; do
    if [[ -e $p ]]; then
      printf '%s\n' "$p"
      return 0
    fi
  done
  return 1
}

# Seed a findable, non-GC'd pre-commit hook in $1 plus the shared scaffolding.
_seed_findable_pre_commit_hook() {
  local hooks_dir="$1"

  # The hook's exec target must be a real executable or the GC check fires.
  _seed_hook_runner_mock

  # Not chmod +x: the code only stats and greps it, and leaving it unexecutable
  # keeps git from ever invoking it during the test's own commits.
  mkdir -p "$hooks_dir"
  printf 'exec %s hook-impl --hook-type=pre-commit\n' "$MOCK_BIN/hook-runner" \
    > "$hooks_dir/pre-commit"

  _seed_pre_commit_hook_check_env
}

@test "_ul_ensure_pre_commit_hooks finds the hook when core.hooksPath is ABSOLUTE" {
  git config core.hooksPath "$TEST_DIR/.git/hooks"
  _seed_findable_pre_commit_hook "$TEST_DIR/.git/hooks"

  source "$UL_LOCKS_LIB"
  # ul_setup sets this before calling; the old implementation joined the resolved
  # hooksPath onto it, so it must be populated for this to test the real defect.
  # shellcheck disable=SC2034  # read by the sourced update-locks-lib
  _UL_SCRIPT_DIR="$TEST_DIR"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook not found" ]]
  [[ ! $output =~ "hook binary missing" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks finds the COMMON hooks dir from a linked worktree with core.hooksPath unset" {
  # Premise: git's normal state, no core.hooksPath anywhere in scope.
  [ -z "$(git config --get core.hooksPath || true)" ]

  # The hook lives in the main repo's COMMON hooks dir, never in the worktree.
  _seed_findable_pre_commit_hook "$TEST_DIR/.git/hooks"

  local wt="$TEST_DIR/linked-wt"
  git worktree add --quiet "$wt" -b feat
  [ -f "$wt/.git" ] # a FILE, not a directory — the reason a relative path fails

  source "$UL_LOCKS_LIB"
  # shellcheck disable=SC2034  # read by the sourced update-locks-lib
  _UL_SCRIPT_DIR="$wt"
  cd "$wt" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook not found" ]]
  [[ ! $output =~ "hook binary missing" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

# --- _ul_ensure_pre_commit_hooks: Tier-2 GC check (bead pg2-hk08h) ---
#
# Tier 2 asks "is the hook's pinned binary still in the store, or was it GC'd?".
# It used to answer that by parsing the hook's `^exec ` line, which assumed the
# store path was the exec TARGET. prek's current template puts the path on its
# own `PREK=` line and execs the VARIABLE, so the parse yielded the literal
# 7-character string `"$PREK"` — never executable — and Tier 2 reported a GC'd
# binary on EVERY run. The check is now a format-agnostic scan for every
# /nix/store path the hook NAMES, wherever it names it, so these tests fix the
# BEHAVIOUR (fires iff a named store path is gone) and not a template shape.

@test "_ul_ensure_pre_commit_hooks Tier 2 does NOT fire when a store path the hook names EXISTS" {
  local store_path=""
  store_path=$(_existing_store_path) || skip "no live /nix/store entry to name"
  [ -e "$store_path" ]

  local hooks_dir="$TEST_DIR/.git/hooks"
  mkdir -p "$hooks_dir"
  # The path is deliberately NOT the exec target: the scan must find it anyway.
  printf 'PINNED="%s"\nexec "$PINNED" hook-impl\n' "$store_path" \
    > "$hooks_dir/pre-commit"
  _seed_pre_commit_hook_check_env

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook not found" ]]
  [[ ! $output =~ "hook binary missing" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks Tier 2 FIRES when a store path the hook names is GONE" {
  local gone="/nix/store/00000000000000000000000000000000-gc-d-1.0/bin/gone"
  [ ! -e "$gone" ]

  # The exec target is a live NON-store executable, so the old exec-line parse
  # saw a healthy hook. Only a scan of what the hook NAMES sees the dead path —
  # this is what stops the fix from neutering the check into always-passing.
  _seed_hook_runner_mock
  local hooks_dir="$TEST_DIR/.git/hooks"
  mkdir -p "$hooks_dir"
  printf 'PINNED="%s"\nexec %s hook-impl\n' "$gone" "$MOCK_BIN/hook-runner" \
    > "$hooks_dir/pre-commit"
  _seed_pre_commit_hook_check_env

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  # `== *…*`, not `=~`: the message's parens are regex metacharacters, and a
  # quoted `=~` RHS that only LOOKS like a regex trips SC2076. This asserts the
  # whole message literally, so the wording itself is locked in.
  [[ $output == *"hook binary missing (GC'd), reinstalling"* ]]
  [ -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks Tier 2 does NOT fire when the hook names NO store path" {
  local hooks_dir="$TEST_DIR/.git/hooks"
  mkdir -p "$hooks_dir"
  # A non-nix install, and equally prek's own PATH fallback shape: there is no
  # pinned store path to validate, so Tier 2 has nothing to say. Note the exec
  # target is a bare command NAME, which the old `-x` test rejected outright.
  printf '#!/bin/sh\nexec pre-commit hook-impl --hook-type=pre-commit\n' \
    > "$hooks_dir/pre-commit"
  _seed_pre_commit_hook_check_env

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook not found" ]]
  [[ ! $output =~ "hook binary missing" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks Tier 2 does NOT fire on prek's CURRENT hook template" {
  local store_path=""
  store_path=$(_existing_store_path) || skip "no live /nix/store entry to name"

  local hooks_dir="$TEST_DIR/.git/hooks"
  mkdir -p "$hooks_dir"
  # Verbatim shape emitted by prek 0.3.11 (--script-version 4), with only the
  # pinned path swapped for a live one: the store path sits on its own `PREK=`
  # line and `exec` runs the VARIABLE. This exact hook is what made the old
  # parse yield `"$PREK"` and reinstall on every run.
  cat > "$hooks_dir/pre-commit" <<HOOK
#!/bin/sh
# File generated by prek: https://github.com/j178/prek
# ID: 182c10f181da4464a3eec51b83331688

HERE="\$(cd "\$(dirname "\$0")" && pwd)"
PREK="$store_path"

# Check if the full path to prek is executable, otherwise fallback to PATH
if [ ! -x "\$PREK" ]; then
    PREK="prek"
fi

exec "\$PREK" hook-impl --hook-dir "\$HERE" --script-version 4 --hook-type=pre-commit --config=".pre-commit-config.yaml" -- "\$@"
HOOK
  _seed_pre_commit_hook_check_env

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook not found" ]]
  [[ ! $output =~ "hook binary missing" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

# --- ul_run_step: success path ---

@test "ul_run_step commits changes on success" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  my_step() { echo "new content" > file.txt; }
  ul_run_step "test-step" "update: test step" my_step

  local msg
  msg=$(git log -1 --format=%s)
  [ "$msg" = "update: test step" ]
}

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
  [[ "$output" == *".update-locks/steps/noop-step"* ]]
  [ "$(git show --name-only --format= HEAD | grep -vc '^$')" -eq 1 ]
}

@test "ul_run_step increments succeeded counter" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  noop_step() { true; }
  ul_run_step "s1" "msg" noop_step
  ul_run_step "s2" "msg" noop_step

  [ "$_UL_STEPS_RAN" -eq 2 ]
  [ "$_UL_STEPS_SUCCEEDED" -eq 2 ]
  [ "$_UL_STEPS_FAILED" -eq 0 ]
}

# --- ul_run_step: success commits content + stamp together ---

@test "ul_run_step success commits content and the stamp in one commit" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  my_step() { echo "new content" > file.txt; }
  ul_run_step "test-step" "update: test step" my_step

  [ "$(git log -1 --format=%s)" = "update: test step" ]
  run git show --name-only --format= HEAD
  [[ "$output" == *"file.txt"* ]]
  [[ "$output" == *".update-locks/steps/test-step"* ]]
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
  [[ "$output" == *".update-locks/steps/defer-step"* ]]
  [[ "$output" != *"file.txt"* ]]
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
  [[ "$output" == *".update-locks/steps/defer-noop"* ]]
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

# --- ul_run_step: failure path ---

@test "ul_run_step cleans up on failure" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  failing_step() { echo "mess" > file.txt; return 1; }
  ul_run_step "fail-step" "should not appear" failing_step

  # Workspace should be clean
  git diff --quiet
  git diff --cached --quiet
}

@test "ul_run_step records failure but does not exit" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  failing_step() { return 1; }
  ul_run_step "fail-step" "msg" failing_step

  [ "$_UL_STEPS_FAILED" -eq 1 ]
  [ "${_UL_FAILED_STEPS[0]}" = "fail-step" ]
}

@test "ul_run_step cleans up untracked files on failure" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  messy_step() { echo "junk" > newfile.txt; return 1; }
  ul_run_step "messy-step" "msg" messy_step

  [ ! -f "$TEST_DIR/newfile.txt" ]
}

@test "ul_run_step continues after failure" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  failing_step() { return 1; }
  succeeding_step() { echo "good" > file.txt; }

  ul_run_step "step1" "msg" failing_step
  ul_run_step "step2" "update: step2" succeeding_step

  [ "$_UL_STEPS_FAILED" -eq 1 ]
  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
  local msg
  msg=$(git log -1 --format=%s)
  [ "$msg" = "update: step2" ]
}

# --- ul_run_step: cd isolation ---

@test "ul_run_step isolates cd in subshell" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  mkdir -p "$TEST_DIR/subdir"
  cd_step() { cd "$TEST_DIR/subdir"; }
  ul_run_step "cd-step" "msg" cd_step

  [ "$(pwd)" = "$TEST_DIR" ]
}

# --- ul_run_step: dirty guard ---

@test "ul_run_step exits script if workspace is dirty" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  # Manually dirty the workspace to simulate broken cleanup
  echo "dirty" > file.txt

  run ul_run_step "step" "msg" true
  [ "$status" -eq 1 ]
  [[ "$output" =~ "dirty" ]]
}

@test "ul_run_step is FATAL when an untracked file appears before a step" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  echo "sneaky" > sneaky.txt # untracked, appears after the setup gate

  run ul_run_step "step" "msg" true
  [ "$status" -eq 1 ]
  [[ "$output" =~ "dirty" ]]
}

@test "ul_run_step commits a NEW file created by the step (git add -A retained)" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  newfile_step() { echo "generated" > generated.lock; }
  ul_run_step "gen-step" "update: gen" newfile_step

  git ls-files --error-unmatch generated.lock # tracked => committed
}

# --- ul_run_step: cache integration ---

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

# --- ul_finalize ---

@test "ul_finalize exits 0 when all steps pass" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  noop() { true; }
  ul_run_step "s1" "msg" noop

  run ul_finalize
  [ "$status" -eq 0 ]
  [[ "$output" =~ "successfully" ]]
}

@test "ul_finalize exits 1 when any step failed" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  fail() { return 1; }
  ul_run_step "bad-step" "msg" fail

  run ul_finalize
  [ "$status" -eq 1 ]
  [[ "$output" =~ "bad-step" ]]
}

@test "ul_finalize reports correct counts" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  pass() { true; }
  fail() { return 1; }
  ul_run_step "s1" "msg" pass
  ul_run_step "s2" "msg" fail

  run ul_finalize
  [[ "$output" =~ "Ran:     2" ]]
  [[ "$output" =~ "Passed:  1" ]]
  [[ "$output" =~ "Failed:  1" ]]
}

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

# --- upgrade summary ---

@test "ul_run_step records a content-changing step as an upgrade" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  changed() { echo "new content" > file.txt; }
  ul_run_step "test-step" "update: test step" changed

  [ "${#_UL_UPGRADED_STEPS[@]}" -eq 1 ]
  [ "${_UL_UPGRADED_STEPS[0]}" = "test-step" ]
}

@test "ul_run_step does NOT record a no-op success as an upgrade" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  noop() { true; }
  ul_run_step "noop-step" "update: noop" noop

  [ "${#_UL_UPGRADED_STEPS[@]}" -eq 0 ]
  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
}

@test "ul_run_step does NOT record a deferral as an upgrade" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  defer() { echo "junk" > file.txt; ul_attempted; }
  ul_run_step "defer-step" "update: defer" defer

  [ "${#_UL_UPGRADED_STEPS[@]}" -eq 0 ]
  [ "$_UL_STEPS_DEFERRED" -eq 1 ]
}

@test "ul_run_step captures a version delta from a .nix change" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  printf '  version = "1.0.0";\n' > pkg.nix
  git add pkg.nix
  git commit -m "add pkg.nix"

  bump() { printf '  version = "1.2.1";\n' > pkg.nix; }
  ul_run_step "update-pkg" "update: pkg" bump

  # Assert old and new versions are both present without embedding the U+2192
  # arrow literal in this .bats file — bats' line preprocessor mishandles the
  # multibyte char in a @test body (the rendered note still uses the arrow).
  [ "${#_UL_UPGRADE_NOTES[@]}" -eq 1 ]
  [[ "${_UL_UPGRADE_NOTES[0]}" == *"1.0.0"* ]]
  [[ "${_UL_UPGRADE_NOTES[0]}" == *"1.2.1"* ]]
}

@test "ul_run_step names changed flake.lock inputs (skips unchanged ones)" {
  command -v jq >/dev/null || skip "jq not available"
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  printf '%s\n' '{"nodes":{"nixpkgs":{"locked":{"rev":"aaaa"}},"home-manager":{"locked":{"rev":"bbbb"}},"root":{}}}' > flake.lock
  git add flake.lock
  git commit -m "add flake.lock"

  bump_lock() {
    printf '%s\n' '{"nodes":{"nixpkgs":{"locked":{"rev":"cccc"}},"home-manager":{"locked":{"rev":"bbbb"}},"root":{}}}' > flake.lock
  }
  ul_run_step "nix-flake-update" "update: lock" bump_lock

  [ "${#_UL_UPGRADE_NOTES[@]}" -eq 1 ]
  [[ "${_UL_UPGRADE_NOTES[0]}" =~ nixpkgs ]]
  [[ ! "${_UL_UPGRADE_NOTES[0]}" =~ home-manager ]]
}

@test "ul_finalize lists upgraded steps and counts them" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  changed() { echo "new content" > file.txt; }
  ul_run_step "test-step" "update: test step" changed

  run ul_finalize
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Upgraded: 1" ]]
  [[ "$output" =~ "Upgrades applied:" ]]
  [[ "$output" =~ "test-step" ]]
  [[ "$output" == *"upgrade(s) applied"* ]]
}

@test "ul_finalize reports zero upgrades when only no-op steps ran" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  noop() { true; }
  ul_run_step "s1" "msg" noop

  run ul_finalize
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Upgraded: 0" ]]
  [[ "$output" =~ "no upgrades" ]]
  [[ ! "$output" =~ "Upgrades applied" ]]
}

# --- signal handling ---

# Note: Tests use SIGTERM (not SIGINT) because POSIX requires background
# processes to have SIGINT set to SIG_IGN, and non-interactive bash cannot
# override this. SIGTERM exercises the same _ul_cleanup trap code path.
# In real usage, Ctrl+C sends SIGINT to the foreground process group, which
# works correctly because the script runs in the foreground.

@test "ul_run_step kills child and cleans up on signal" {
  local ready_fifo="$MOCK_BIN/step-ready"
  mkfifo "$ready_fifo"

  # Driver lives OUTSIDE the git tree (in $MOCK_BIN, like the fifo): if it were
  # an untracked file in $TEST_DIR, ul_setup's clean-tree gate would reject it
  # and slow_step would never signal ready, deadlocking the read below (pg2-31h9y).
  cat > "$MOCK_BIN/signal-test.bash" <<SCRIPT
#!/usr/bin/env bash
export PATH="$MOCK_BIN:\$PATH"
export XDG_STATE_HOME="$XDG_STATE_HOME"
export NIX_UL_FORCE_UPDATE="true"
source "$UL_LOCKS_LIB"
ul_setup "test-project" "$TEST_DIR"
slow_step() { echo "dirty" > file.txt; echo ready > "$ready_fifo"; sleep 60; }
ul_run_step "slow" "msg" slow_step
SCRIPT
  _fix_mock_shebang "$MOCK_BIN/signal-test.bash"
  chmod +x "$MOCK_BIN/signal-test.bash"

  bash "$MOCK_BIN/signal-test.bash" &
  local script_pid=$!
  read -r < "$ready_fifo"
  kill -TERM "$script_pid"
  local rc=0
  wait "$script_pid" 2>/dev/null || rc=$?

  # Exit status should be 143 (128 + 15 for SIGTERM)
  [ "$rc" -eq 143 ]

  # Workspace should be clean (trap cleaned up)
  cd "$TEST_DIR"
  git diff --quiet
  git diff --cached --quiet
}

@test "ul_run_step restores fsmonitor after signal" {
  git config core.fsmonitor true

  local ready_fifo="$MOCK_BIN/step-ready"
  mkfifo "$ready_fifo"

  # Driver lives OUTSIDE the git tree (in $MOCK_BIN, like the fifo): if it were
  # an untracked file in $TEST_DIR, ul_setup's clean-tree gate would reject it
  # and slow_step would never signal ready, deadlocking the read below (pg2-31h9y).
  cat > "$MOCK_BIN/signal-test.bash" <<SCRIPT
#!/usr/bin/env bash
export PATH="$MOCK_BIN:\$PATH"
export XDG_STATE_HOME="$XDG_STATE_HOME"
export NIX_UL_FORCE_UPDATE="true"
source "$UL_LOCKS_LIB"
ul_setup "test-project" "$TEST_DIR"
slow_step() { echo ready > "$ready_fifo"; sleep 60; }
ul_run_step "slow" "msg" slow_step
SCRIPT
  _fix_mock_shebang "$MOCK_BIN/signal-test.bash"
  chmod +x "$MOCK_BIN/signal-test.bash"

  bash "$MOCK_BIN/signal-test.bash" &
  local script_pid=$!
  read -r < "$ready_fifo"
  kill -TERM "$script_pid"
  wait "$script_pid" 2>/dev/null || true

  cd "$TEST_DIR"
  local val
  val=$(git config core.fsmonitor)
  [ "$val" = "true" ]
}

# --- fsmonitor disable/restore scoping ---
#
# These tests pin the SCOPE of the dance. Two different values matter and must
# not be conflated: the EFFECTIVE (merged) value decides WHETHER the dance is
# needed, while the repo-LOCAL value decides HOW to undo it. Conflating them
# converts a user's GLOBAL setting into a permanent per-repo pin (bead
# pg2-znsmo; the split state recorded in pg2-pi5u1 is the symptom).
#
# They drive _ul_disable_fsmonitor / _ul_restore_fsmonitor directly rather than
# through ul_setup, deliberately: ul_setup's clean-tree gate refreshes the index,
# and an index refresh with fsmonitor live spawns git's native daemon — which is
# wedged on some setups (bead pg2-mgcv5), hanging the run outright. These tests
# only ever invoke `git config`, so they are safe and fast everywhere.
#
# The shared setup() already pins GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM to
# /dev/null (bead pg2-klyn6), so the NEUTRAL "no global value" case needs nothing
# here. A test that needs a global value present OPTS IN by repointing
# GIT_CONFIG_GLOBAL at a temp file of its own — never at the real ~/.gitconfig.

@test "_ul_restore_fsmonitor unsets the local key when the value came from global config" {
  local global_cfg="$STATE_DIR/gitconfig"
  printf '[core]\n\tfsmonitor = true\n' > "$global_cfg"
  export GIT_CONFIG_GLOBAL="$global_cfg"

  # Precondition: enabled via global only, with no repo-local key at all.
  [ "$(git config --type=bool --get core.fsmonitor)" = "true" ]
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  # The dance ran: locally disabled for the duration of the run.
  [ "$(git config --local --get core.fsmonitor)" = "false" ]

  _ul_restore_fsmonitor

  # The local key must be GONE, not pinned to true. The `true` was inherited, so
  # writing it back locally would pin a global setting into this repo forever.
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]
  # ...and the outer scope governs again.
  [ "$(git config --type=bool --get core.fsmonitor)" = "true" ]
}

@test "_ul_restore_fsmonitor restores a pre-existing repo-local value verbatim" {
  git config core.fsmonitor true

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor
  [ "$(git config --local --get core.fsmonitor)" = "false" ]

  _ul_restore_fsmonitor

  # A genuinely local value is the repo's own state — put it back.
  [ "$(git config --local --get core.fsmonitor)" = "true" ]
}

@test "_ul_disable_fsmonitor handles a non-canonical boolean value" {
  # git accepts yes/on/1 as boolean true and spawns the native daemon for them
  # exactly as for `true`, so a string compare against "true" would skip the
  # dance and leave a live .ipc socket to break flake evaluation.
  git config core.fsmonitor yes

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor
  [ "$(git config --local --get core.fsmonitor)" = "false" ]

  _ul_restore_fsmonitor

  # Restored verbatim, not normalised to "true".
  [ "$(git config --local --get core.fsmonitor)" = "yes" ]
}

@test "_ul_disable_fsmonitor is a no-op when fsmonitor is disabled" {
  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  # No local key invented for a repo that never had fsmonitor on.
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]

  _ul_restore_fsmonitor
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]
}

@test "_ul_disable_fsmonitor leaves a hook-path fsmonitor untouched" {
  # A hook-based fsmonitor (what the WS1 design on pg2-mgcv5 plans for the ZR
  # monorepo) runs no native daemon and creates no .ipc socket, so it needs no
  # dance — and rewriting the value would destroy the hook path.
  local hook="/path/to/fsmonitor-watchman.sample"
  git config core.fsmonitor "$hook"

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor
  [ "$(git config --local --get core.fsmonitor)" = "$hook" ]

  _ul_restore_fsmonitor
  [ "$(git config --local --get core.fsmonitor)" = "$hook" ]
}

@test "_ul_disable_fsmonitor removes a stale socket even when fsmonitor is disabled" {
  # A socket left behind by an earlier crashed run makes `nix flake` import fail
  # with "unsupported type" regardless of the current config, so its removal must
  # NOT be gated on the dance.
  touch "$TEST_DIR/.git/fsmonitor--daemon.ipc"

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ ! -e "$TEST_DIR/.git/fsmonitor--daemon.ipc" ]
}

@test "ul_setup performs the fsmonitor disable" {
  local global_cfg="$STATE_DIR/gitconfig"
  printf '[core]\n\tfsmonitor = true\n' > "$global_cfg"
  export GIT_CONFIG_GLOBAL="$global_cfg"

  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"

  # Wiring check: ul_setup disabled fsmonitor before reaching its clean-tree gate.
  [ "$(git config --local --get core.fsmonitor)" = "false" ]

  # Disarm the armed cleanup trap and leave fsmonitor OFF. _ul_cleanup runs
  # `git status`; letting the trap restore fsmonitor first would refresh the index
  # with the native daemon live and hang teardown (bead pg2-mgcv5). The opted-in
  # global config needs no reset — bats runs each test in its own process, so this
  # export cannot leak into a sibling test.
  trap - EXIT INT TERM
}

# --- harness hermeticity guard ---

@test "setup() neutralises an ambient global core.fsmonitor (pg2-klyn6 regression guard)" {
  # The pg2-klyn6 guard, mirroring TestHarnessNeutralizesGlobalFsmonitor from the
  # pg2-39rz2 Go fix: prove the harness never inherits the developer's global git
  # config. Plant a SIMULATED developer global config that turns core.fsmonitor on
  # — the setting that, on an affected machine, made every temp repo spawn `git
  # fsmonitor--daemon` and hang the suite — at both locations git looks for a
  # global config, then assert git in this test's repo still sees it unset.
  #
  # The simulation is via HOME / XDG_CONFIG_HOME rather than GIT_CONFIG_GLOBAL,
  # deliberately: that is the exact path by which the real defect enters, and it
  # is what setup()'s GIT_CONFIG_GLOBAL=/dev/null outranks. Drop that export from
  # setup() and this test reads back "true" and fails. The developer's real
  # ~/.gitconfig is never written — only these temp copies, outside TEST_DIR.
  local fake_home="$STATE_DIR/fake-home"
  mkdir -p "$fake_home/.config/git"
  printf '[core]\n\tfsmonitor = true\n' > "$fake_home/.gitconfig"
  cp "$fake_home/.gitconfig" "$fake_home/.config/git/config"
  export HOME="$fake_home"
  export XDG_CONFIG_HOME="$fake_home/.config"

  # CONFIG READ ONLY — never `git status`. `git config` merges config without
  # touching the index, so this assertion cannot itself spawn an fsmonitor daemon;
  # a guard that hung the suite it protects would be worse than no guard at all.
  # `--default false` so an unset key reads back as "false" instead of exiting 1.
  [ "$(git config --default false --type=bool --get core.fsmonitor)" = "false" ]

  # The SYSTEM half cannot be simulated the same way — /etc/gitconfig and git's
  # compiled-in prefix are not writable by the test (and must not be), so assert
  # the neutralisation directly.
  [ "${GIT_CONFIG_SYSTEM:-}" = "/dev/null" ]
}

# --- ul_reexec_in_dev_shell ---

@test "ul_reexec_in_dev_shell returns 0 without exec when IN_NIX_SHELL is set" {
  source "$UL_LOCKS_LIB"
  export IN_NIX_SHELL=impure

  run bash -c "
    export IN_NIX_SHELL=impure
    source '$UL_LOCKS_LIB'
    ul_reexec_in_dev_shell
    echo POST_CALL
  "
  [ "$status" -eq 0 ]
  [[ "$output" =~ "already in nix shell" ]]
  [[ "$output" =~ "POST_CALL" ]]
}

@test "ul_reexec_in_dev_shell falls back to host tools when the dev shell cannot start" {
  # nix develop exits non-zero WITHOUT running the --command, so the sentinel
  # survives -> ul_reexec treats the shell as broken and returns 0 (host tools).
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  echo "nix: broken flake" >&2
  exit 1
fi
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  run bash -c "
    unset IN_NIX_SHELL
    source '$UL_LOCKS_LIB'
    ul_reexec_in_dev_shell
    echo POST_CALL
  "
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARNING" ]]
  [[ "$output" =~ "falling back" ]]
  [[ "$output" =~ "POST_CALL" ]]
}

@test "ul_reexec_in_dev_shell enters the shell once, propagates success, exports UL_LIB_DIR" {
  # A real entry removes the sentinel and runs the command. The mock simulates
  # that (single 'develop' call), echoes the UL_LIB_DIR it inherited, exits 0.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  rm -f "$UL_DEVSHELL_SENTINEL"
  echo "ENTERED uldir=$UL_LIB_DIR"
  exit 0
fi
exit 99
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$TEST_DIR/wrap-test.sh" <<SCRIPT
#!/usr/bin/env bash
source "$UL_LOCKS_LIB"
ul_reexec_in_dev_shell "\$@"
echo FALLTHROUGH
SCRIPT
  _fix_mock_shebang "$TEST_DIR/wrap-test.sh"
  chmod +x "$TEST_DIR/wrap-test.sh"

  run env -u IN_NIX_SHELL UL_LIB_DIR=/resolved/lib/scripts "$TEST_DIR/wrap-test.sh" arg1
  [ "$status" -eq 0 ]
  [[ "$output" =~ "entering dev shell" ]]
  [[ "$output" =~ "ENTERED uldir=/resolved/lib/scripts" ]]
  [[ ! "$output" =~ "WARNING" ]]
  [[ ! "$output" =~ "FALLTHROUGH" ]]
}

@test "ul_reexec_in_dev_shell propagates a non-zero status from inside the shell" {
  # Entry succeeds (sentinel removed) but the in-shell run fails -> that status
  # must propagate, not be masked by the host-tools fallback.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  rm -f "$UL_DEVSHELL_SENTINEL"
  exit 7
fi
exit 99
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$TEST_DIR/wrap-test.sh" <<SCRIPT
#!/usr/bin/env bash
source "$UL_LOCKS_LIB"
ul_reexec_in_dev_shell "\$@"
echo FALLTHROUGH
SCRIPT
  _fix_mock_shebang "$TEST_DIR/wrap-test.sh"
  chmod +x "$TEST_DIR/wrap-test.sh"

  run env -u IN_NIX_SHELL "$TEST_DIR/wrap-test.sh"
  [ "$status" -eq 7 ]
  [[ ! "$output" =~ "WARNING" ]]
  [[ ! "$output" =~ "FALLTHROUGH" ]]
}

@test "ul_reexec_in_dev_shell fallback survives the caller's set -e" {
  # Regression: consumer update-locks.sh scripts run under `set -euo pipefail`.
  # A failing `nix develop` (absent/broken flake, or a devShell that cannot build
  # on this host) must NOT abort the script before the sentinel/fallback check —
  # the `|| rc=$?` guard keeps errexit from firing so host tooling still runs.
  # The pre-existing fallback test above runs in a bare `bash -c` with no set -e,
  # so it does not exercise this path.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  echo "nix: devShell cannot build on this host" >&2
  exit 1
fi
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$TEST_DIR/setE-test.sh" <<SCRIPT
#!/usr/bin/env bash
set -euo pipefail
source "$UL_LOCKS_LIB"
ul_reexec_in_dev_shell "\$@"
echo REACHED_HOST_TOOLS
SCRIPT
  _fix_mock_shebang "$TEST_DIR/setE-test.sh"
  chmod +x "$TEST_DIR/setE-test.sh"

  run env -u IN_NIX_SHELL "$TEST_DIR/setE-test.sh"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "falling back" ]]
  [[ "$output" =~ "REACHED_HOST_TOOLS" ]]
}

@test "ul_reexec_in_dev_shell enters the flake dir named by UL_FLAKE_DIR" {
  # A subdir-flake consumer (e.g. homelab's nix/) points `nix develop` at
  # UL_FLAKE_DIR instead of the script's directory. The mock records its target.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  echo "DEVELOP_TARGET=$2" >&2
  rm -f "$UL_DEVSHELL_SENTINEL"
  exit 0
fi
exit 99
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$TEST_DIR/flakedir-test.sh" <<SCRIPT
#!/usr/bin/env bash
source "$UL_LOCKS_LIB"
ul_reexec_in_dev_shell "\$@"
echo FALLTHROUGH
SCRIPT
  _fix_mock_shebang "$TEST_DIR/flakedir-test.sh"
  chmod +x "$TEST_DIR/flakedir-test.sh"

  run env -u IN_NIX_SHELL UL_FLAKE_DIR=/some/repo/nix "$TEST_DIR/flakedir-test.sh"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "entering dev shell at /some/repo/nix" ]]
  [[ "$output" =~ "DEVELOP_TARGET=/some/repo/nix" ]]
  [[ ! "$output" =~ "FALLTHROUGH" ]]
}

# ---------------------------------------------------------------------------
# ul_classify_step_failure — failure signature classification (ADR 0020)
# ---------------------------------------------------------------------------

@test "ul_classify_step_failure: ENOSPC -> resource" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'error: write of 1113 bytes: No space left on device\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$status" -eq 0 ]
  [ "$output" = "resource" ]
}

@test "ul_classify_step_failure: could not resolve host -> transient" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'fatal: unable to access ...: Could not resolve host: github.com\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "transient" ]
}

@test "ul_classify_step_failure: TLS handshake timeout -> transient" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'net/http: TLS handshake timeout\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "transient" ]
}

@test "ul_classify_step_failure: HTTP 503 -> transient" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf "error: unable to download 'https://x/y.tar.gz': HTTP error 503\n" > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "transient" ]
}

@test "ul_classify_step_failure: git remote hung up -> transient" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'fatal: The remote end hung up unexpectedly\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "transient" ]
}

@test "ul_classify_step_failure: HTTP 404 broken pin stays hard (not transient)" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf "error: unable to download 'https://x/y.tar.gz': HTTP error 404\n" > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: generic build failure stays hard" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf "error: builder for '/nix/store/x.drv' failed with exit code 1\n" > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: OOM stays hard (not resource, not transient)" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'fatal error: runtime: cannot allocate memory\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: resource wins over co-occurring network noise" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'Could not resolve host: x\nNo space left on device\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "resource" ]
}

@test "ul_classify_step_failure: hash mismatch wins over co-occurring transient blip" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf 'warning: Could not resolve host: cache.nixos.org (retrying)\nerror: hash mismatch in fixed-output derivation\n' > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: 404 broken pin wins over a co-occurring 503 retry" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf "error: unable to download 'x': HTTP error 503 (retrying)\nerror: unable to download 'x': HTTP error 404\n" > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: builder failure wins over a co-occurring connection reset" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); printf "read: connection reset by peer\nerror: builder for '/nix/store/x.drv' failed with exit code 1\n" > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

@test "ul_classify_step_failure: empty stderr -> hard" {
  source "$UL_LOCKS_LIB"
  f=$(mktemp); : > "$f"
  run ul_classify_step_failure "$f"
  [ "$output" = "hard" ]
}

# ---------------------------------------------------------------------------
# ul_run_step — transient / resource classification of a failed step
# ---------------------------------------------------------------------------

@test "ul_run_step streams step stdout+stderr live while capturing stderr" {
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    noisy() { echo "OUT-LINE"; echo "ERR-LINE-XYZZY" >&2; echo c > file.txt; }
    ul_run_step "noisy" "update: noisy" noisy
  '
  [ "$status" -eq 0 ]
  [[ "$output" == *"OUT-LINE"* ]]
  [[ "$output" == *"ERR-LINE-XYZZY"* ]]
}

@test "ul_run_step transient failure: rollback, NO stamp, no fail, run continues" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  before=$(git rev-parse HEAD)
  net_fail() { echo "junk" > file.txt; echo "fatal: Could not resolve host: github.com" >&2; return 1; }
  ul_run_step "net-step" "update: net" net_fail
  [ "$(git rev-parse HEAD)" = "$before" ]                        # no commit
  [ ! -f "$TEST_DIR/.update-locks/steps/net-step" ]              # NO stamp (retry next run)
  [ "$(cat file.txt)" = "initial" ]                             # content rolled back
  git diff --quiet
  git diff --cached --quiet
  [ "$_UL_STEPS_TRANSIENT" -eq 1 ]
  [ "$_UL_STEPS_FAILED" -eq 0 ]
}

@test "ul_run_step: transient step defers but a later successful step still commits" {
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  net_fail() { echo "boom" > file.txt; echo "The remote end hung up unexpectedly" >&2; return 1; }
  good_step() { echo "updated" > file.txt; }
  ul_run_step "net-step" "update: net" net_fail
  ul_run_step "good-step" "update: good" good_step
  [ "$_UL_STEPS_TRANSIENT" -eq 1 ]
  [ "$_UL_STEPS_FAILED" -eq 0 ]
  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
  run git log -1 --format=%s
  [ "$output" = "update: good" ]
  [ "$(cat "$TEST_DIR/file.txt")" = "updated" ]
}

@test "ul_run_step transient does not make ul_finalize exit non-zero; summary shows Transient" {
  # Run the whole sequence in a sub-bash: ul_run_step backgrounds a tee for
  # stderr capture, and following a direct ul_run_step with a second `run` in the
  # same test trips bats' fd/accounting. The sub-bash isolates it (as tests 52/56
  # do) and its exit status IS ul_finalize's, which is what we assert.
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    net_fail() { echo "dial tcp 1.2.3.4:443: i/o timeout" >&2; return 1; }
    ul_run_step "net-step" "update: net" net_fail
    ul_finalize
  '
  [ "$status" -eq 0 ]
  [[ "$output" == *"Transient: 1"* ]]
  [[ "$output" == *"Failed:  0"* ]]
}

# --- ul_finalize: machine-readable UL_RESULT line (bash↔Go boundary, ADR 0020) ---

@test "ul_finalize emits UL_RESULT transient=N so pn sees the transient count (green path)" {
  # A green run with a transient step: exit 0, but the machine-readable line
  # carries the transient count pn cannot otherwise see (see sub-bash rationale
  # on the test above).
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    net_fail() { echo "dial tcp 1.2.3.4:443: i/o timeout" >&2; return 1; }
    ul_run_step "net-step" "update: net" net_fail
    ul_finalize
  '
  [ "$status" -eq 0 ]
  [[ "$output" == *"UL_RESULT transient=1"* ]]
}

@test "ul_finalize emits UL_RESULT transient=0 when nothing was transient" {
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    noop() { true; }
    ul_run_step "s1" "msg" noop
    ul_finalize
  '
  [ "$status" -eq 0 ]
  [[ "$output" == *"UL_RESULT transient=0"* ]]
}

@test "ul_finalize emits UL_RESULT on the failure (exit 1) path too" {
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    fail() { return 1; }
    ul_run_step "bad-step" "msg" fail
    ul_finalize
  '
  [ "$status" -eq 1 ]
  [[ "$output" == *"UL_RESULT transient=0"* ]]
}

@test "ul_run_step resource failure aborts update-locks with UL_RC_ABORT (77)" {
  run bash -c '
    source "'"$UL_LOCKS_LIB"'"
    ul_setup "test-project" "'"$TEST_DIR"'" >/dev/null 2>&1
    disk_fail() { echo "x" > file.txt; echo "error: write of 9 bytes: No space left on device" >&2; return 1; }
    ul_run_step "disk-step" "update: disk" disk_fail
    echo "SHOULD-NOT-REACH"
  '
  [ "$status" -eq 77 ]
  [[ "$output" != *"SHOULD-NOT-REACH"* ]]
  [[ "$output" == *"disk full"* || "$output" == *"No space left"* ]]
}

@test "ul_setup aborts with 77 when the nix daemon health check fails" {
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *eval*--expr*) exit 1 ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  run bash -c 'source "'"$UL_LOCKS_LIB"'"; ul_setup "p" "'"$TEST_DIR"'"'
  [ "$status" -eq 77 ]
}
