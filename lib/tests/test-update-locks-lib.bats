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
_fix_mock_shebang() {
  sed -i "1s|.*|#!$(command -v bash)|" "$1"
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

  cd "$TEST_DIR"
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
  [[ "$output" =~ ".update-locks/steps/noop-step" ]]
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

# --- signal handling ---

# Note: Tests use SIGTERM (not SIGINT) because POSIX requires background
# processes to have SIGINT set to SIG_IGN, and non-interactive bash cannot
# override this. SIGTERM exercises the same _ul_cleanup trap code path.
# In real usage, Ctrl+C sends SIGINT to the foreground process group, which
# works correctly because the script runs in the foreground.

@test "ul_run_step kills child and cleans up on signal" {
  local ready_fifo="$MOCK_BIN/step-ready"
  mkfifo "$ready_fifo"

  cat > "$TEST_DIR/signal-test.bash" <<SCRIPT
#!/usr/bin/env bash
export PATH="$MOCK_BIN:\$PATH"
export XDG_STATE_HOME="$XDG_STATE_HOME"
export NIX_UL_FORCE_UPDATE="true"
source "$UL_LOCKS_LIB"
ul_setup "test-project" "$TEST_DIR"
slow_step() { echo "dirty" > file.txt; echo ready > "$ready_fifo"; sleep 60; }
ul_run_step "slow" "msg" slow_step
SCRIPT
  _fix_mock_shebang "$TEST_DIR/signal-test.bash"
  chmod +x "$TEST_DIR/signal-test.bash"

  bash "$TEST_DIR/signal-test.bash" &
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

  cat > "$TEST_DIR/signal-test.bash" <<SCRIPT
#!/usr/bin/env bash
export PATH="$MOCK_BIN:\$PATH"
export XDG_STATE_HOME="$XDG_STATE_HOME"
export NIX_UL_FORCE_UPDATE="true"
source "$UL_LOCKS_LIB"
ul_setup "test-project" "$TEST_DIR"
slow_step() { echo ready > "$ready_fifo"; sleep 60; }
ul_run_step "slow" "msg" slow_step
SCRIPT
  _fix_mock_shebang "$TEST_DIR/signal-test.bash"
  chmod +x "$TEST_DIR/signal-test.bash"

  bash "$TEST_DIR/signal-test.bash" &
  local script_pid=$!
  read -r < "$ready_fifo"
  kill -TERM "$script_pid"
  wait "$script_pid" 2>/dev/null || true

  cd "$TEST_DIR"
  local val
  val=$(git config core.fsmonitor)
  [ "$val" = "true" ]
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
