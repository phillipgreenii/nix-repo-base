#!/usr/bin/env bats
# bats file_tags=type:unit
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
  # Hermetic git fixture (bead pg2-blc3y; rule "Tests That Need Git"): the shared
  # harness owns the fixture root, the fresh empty HOME, the neutralised system
  # git config, the GIT_CEILING_DIRECTORIES boundary and the allowlist env
  # rebuild (which also drops any GIT_DIR-family and GIT_CONFIG_COUNT/KEY_n/
  # VALUE_n entry a pre-commit hook run injected, and any TRACEPARENT /
  # PN_NIX_LOG_* hand-off from the developer's own session), so this suite
  # carries no GIT_* scrub of its own. In the nix check the harness is found
  # next to the library under test (UL_LIB_SCRIPTS_DIR); locally it is the
  # repo's canonical copy.
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${UL_LIB_SCRIPTS_DIR:-$BATS_TEST_DIRNAME/../scripts}/git-fixture-harness.bash"

  # gfh_setup rebuilds the exported environment from an allowlist, which would
  # drop UL_LIB_SCRIPTS_DIR (injected by the nix check).
  gfh_save_env UL_LIB_SCRIPTS_DIR
  gfh_setup "test-update-locks-lib"
  gfh_restore_env

  # TEST_DIR is the git working tree (GFH_REPO); everything else below lives
  # beside it under GFH_WORK, outside the tree, so `git add -A` in a step's
  # commit never sweeps it into the per-step stamp commits.
  TEST_DIR="$GFH_REPO"
  # XDG_STATE_HOME must live OUTSIDE the repo: ul_init writes the per-step stamps
  # under it.
  STATE_DIR="$GFH_WORK/state"
  mkdir -p "$STATE_DIR"
  export XDG_STATE_HOME="$STATE_DIR"
  export NIX_UL_FORCE_UPDATE="true"

  # Mock nix so that `nix fmt` is a no-op in tests
  # (real nix fmt requires treefmt/flake context not available in test sandbox)
  # Mock lives OUTSIDE TEST_DIR to survive `git clean -fd` inside test steps
  MOCK_BIN="$GFH_WORK/mock-bin"
  mkdir -p "$MOCK_BIN"
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  export PATH="$MOCK_BIN:$PATH"

  # Mock pg-hooks: by default it reports `present`, so a test that is not about
  # the hook bundle never installs one regardless of whether the developer's
  # machine has a real pg-hooks on PATH. Bundle tests overwrite the state through
  # UL_TEST_PG_HOOKS_STATE (empty: print nothing, as a machine without pg-hooks).
  cat > "$MOCK_BIN/pg-hooks" <<'MOCK'
#!/usr/bin/env bash
if [[ $1 == status && $2 == --porcelain ]]; then
  s="${UL_TEST_PG_HOOKS_STATE-present}"
  [[ -n $s ]] && echo "state=$s"
  echo "bundle="
  echo "generation="
  echo "stages="
  echo "reinstall="
fi
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/pg-hooks"
  chmod +x "$MOCK_BIN/pg-hooks"

  cd "$TEST_DIR" || return 1
}

teardown() {
  cd /
  gfh_teardown
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

@test "ul_setup installs a stale hook bundle without dirtying the tree and passes the gate" {
  # The bundle lives under the git common dir, never in the working tree, so
  # (re)installing it must NOT dirty the tracked tree, must NOT be committed, and
  # ul_setup must pass the clean-tree gate.
  export UL_TEST_PG_HOOKS_STATE=stale

  # nix mock: `build .#install-pre-commit-hooks` succeeds; `run
  # .#install-pre-commit-hooks` records the install inside .git; all else
  # (eval/fmt) is a silent no-op.
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *run*install-pre-commit-hooks*) echo installed > "$(git rev-parse --git-common-dir)/bundle-installed" ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  local before_hash
  before_hash=$(git rev-parse HEAD)

  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR" # must NOT exit 1

  [ "$(cat "$(git rev-parse --git-common-dir)/bundle-installed")" = installed ]
  git diff --quiet          # tracked working tree clean
  git diff --cached --quiet # nothing staged
  # No pre-commit commit was made — HEAD is unchanged.
  [ "$(git rev-parse HEAD)" = "$before_hash" ]
}

@test "ul_setup still exits 1 when a file is dirty alongside a bundle install" {
  # Installing the bundle must not mask a genuine uncommitted edit to a tracked
  # file: the gate must still fire, and the edit must not be destroyed on the
  # gate-fail path.
  export UL_TEST_PG_HOOKS_STATE=stale
  echo "user edit" > file.txt # genuine uncommitted work

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
  # no cleanup trap is armed yet (the full cleanup trap is armed only AFTER the
  # gate).
  [ -f "$TEST_DIR/untracked.txt" ]
  [ "$(cat "$TEST_DIR/untracked.txt")" = "precious user data" ]
}

# --- _ul_ensure_pre_commit_hooks: hook bundle ---
#
# `pg-hooks status --porcelain` decides: present does nothing; stale, broken,
# relocated and missing reinstall from the canonical clone (and only report from
# a linked worktree); unreachable and "no pg-hooks" report and continue. There is
# no legacy audit: a clone holding only an old .pre-commit-config.yaml reports
# `missing` and is treated like any other missing bundle.

# Seed the scaffolding every test here shares: a nix mock whose
# `run .#install-pre-commit-hooks` is OBSERVABLE via a marker file.
_seed_pre_commit_hook_check_env() {
  UL_TEST_REINSTALL_MARKER="$STATE_DIR/reinstall-ran"
  export UL_TEST_REINSTALL_MARKER
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *run*install-pre-commit-hooks*) : > "$UL_TEST_REINSTALL_MARKER" ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
}

_run_ensure_in_bundle_mode() {
  UL_TEST_PG_HOOKS_STATE="$1"
  export UL_TEST_PG_HOOKS_STATE
  _seed_pre_commit_hook_check_env
  source "$UL_LOCKS_LIB"
  # shellcheck disable=SC2034  # read by the sourced update-locks-lib
  _UL_SCRIPT_DIR="$PWD"
  run _ul_ensure_pre_commit_hooks
}

@test "_ul_ensure_pre_commit_hooks bundle mode: present does not reinstall" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode present
  [ "$status" -eq 0 ]
  [[ $output =~ "hook bundle state: present" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: stale reinstalls from the canonical clone" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode stale
  [ "$status" -eq 0 ]
  [[ $output =~ "hook bundle stale, reinstalling" ]]
  [ -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: broken and relocated reinstall from the canonical clone" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode broken
  [ "$status" -eq 0 ]
  [ -e "$UL_TEST_REINSTALL_MARKER" ]
  rm -f "$UL_TEST_REINSTALL_MARKER"
  _run_ensure_in_bundle_mode relocated
  [ "$status" -eq 0 ]
  [ -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: missing reinstalls from the canonical clone" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode missing
  [ "$status" -eq 0 ]
  [[ $output =~ "hook bundle missing, reinstalling" ]]
  [ -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: stale or missing in a linked worktree reports and does NOT install" {
  local wt="$TEST_DIR/linked-wt"
  git worktree add --quiet "$wt" -b feat
  cd "$wt" || return 1
  _run_ensure_in_bundle_mode stale
  [ "$status" -eq 0 ]
  [[ $output =~ "linked worktree" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
  _run_ensure_in_bundle_mode missing
  [ "$status" -eq 0 ]
  [[ $output =~ "linked worktree" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: no state (no pg-hooks) warns and continues without installing" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode ""
  [ "$status" -eq 0 ]
  [[ ! $output =~ "hook bundle state" ]]
  [[ $output =~ "pg-hooks not installed" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks bundle mode: unreachable warns and does not install" {
  cd "$TEST_DIR" || return 1
  _run_ensure_in_bundle_mode unreachable
  [ "$status" -eq 0 ]
  [[ $output =~ "does not run hooks" ]]
  [ ! -e "$UL_TEST_REINSTALL_MARKER" ]
}

@test "_ul_ensure_pre_commit_hooks skips silently when the flake lacks install-pre-commit-hooks" {
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "error: flake does not provide attribute 'packages.x.install-pre-commit-hooks'" >&2
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "_ul_ensure_pre_commit_hooks surfaces a genuine install-pre-commit-hooks build failure" {
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "error: builder for foo failed" >&2
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [[ $output == *"failed (not an attr-missing error)"* ]]
  [[ $output == *"builder for foo failed"* ]]
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

# --- ul_run_step: pre-commit-hooks refresh on a flake.lock-changing step
# (bd pg2-vayo4) ---
#
# ul_setup's own _ul_ensure_pre_commit_hooks call happens ONCE, at the START
# of the run, against whatever flake.lock is checked out then. If a later
# step (canonically "nix-flake-update") bumps flake.lock, the derivation that
# install produced is now stale relative to the lock this step just moved.
# _ul_commit_updated must re-run _ul_ensure_pre_commit_hooks, before its own
# commit, whenever the step's own (still-uncommitted) changes touched
# flake.lock -- and must NOT do so for a step that left flake.lock untouched.
#
# The mock pg-hooks below plays the bundle stamp: it reports `present` while the
# CURRENT flake.lock matches the content the last install saw, and `stale`
# otherwise -- mirroring how the real bundle stamp covers flake.lock. The mock
# nix `run .#install-pre-commit-hooks` records the lock it installed against.
_seed_lock_sensitive_pre_commit_mock() {
  UL_TEST_INSTALL_COUNTER="$STATE_DIR/install-count"
  UL_TEST_INSTALLED_LOCK="$STATE_DIR/installed-lock-cksum"
  export UL_TEST_INSTALL_COUNTER UL_TEST_INSTALLED_LOCK

  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
case "$*" in
  *run*install-pre-commit-hooks*)
    n=$(( $(cat "$UL_TEST_INSTALL_COUNTER" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$UL_TEST_INSTALL_COUNTER"
    cksum flake.lock > "$UL_TEST_INSTALLED_LOCK"
    ;;
esac
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$MOCK_BIN/pg-hooks" <<'MOCK'
#!/usr/bin/env bash
if [[ $1 == status && $2 == --porcelain ]]; then
  if [[ -f $UL_TEST_INSTALLED_LOCK && "$(cksum flake.lock)" == "$(cat "$UL_TEST_INSTALLED_LOCK")" ]]; then
    echo "state=present"
  else
    echo "state=stale"
  fi
fi
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/pg-hooks"
  chmod +x "$MOCK_BIN/pg-hooks"

  printf '%s\n' '{"nodes":{"nixpkgs":{"locked":{"rev":"aaaa"}},"root":{}}}' > flake.lock
  git add flake.lock
  git commit -m "add flake.lock"
}

@test "ul_run_step re-installs pre-commit hooks when a step's own changes touch flake.lock (pg2-vayo4)" {
  _seed_lock_sensitive_pre_commit_mock

  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  [ "$(cat "$UL_TEST_INSTALL_COUNTER")" -eq 1 ] # ul_setup's own install

  bump_lock() {
    printf '%s\n' '{"nodes":{"nixpkgs":{"locked":{"rev":"bbbb"}},"root":{}}}' > flake.lock
  }
  ul_run_step "nix-flake-update" "update: lock" bump_lock

  [ "$_UL_STEPS_FAILED" -eq 0 ]
  # The step changed flake.lock, so the mock bundle now reports stale --
  # _ul_commit_updated must have re-run _ul_ensure_pre_commit_hooks before
  # committing, ahead of any git-hook run the commit triggers.
  [ "$(cat "$UL_TEST_INSTALL_COUNTER")" -eq 2 ]
}

@test "ul_run_step does NOT re-install pre-commit hooks for a step that leaves flake.lock untouched" {
  _seed_lock_sensitive_pre_commit_mock

  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  [ "$(cat "$UL_TEST_INSTALL_COUNTER")" -eq 1 ] # ul_setup's own install

  other_step() { echo "new content" > file.txt; }
  ul_run_step "other-step" "update: other" other_step

  [ "$_UL_STEPS_FAILED" -eq 0 ]
  # flake.lock never moved, so no re-install was warranted or should happen.
  [ "$(cat "$UL_TEST_INSTALL_COUNTER")" -eq 1 ]
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

# --- fsmonitor disable (env-only; nothing is written to git config) ---
#
# _ul_disable_fsmonitor turns fsmonitor off through GIT_CONFIG_COUNT/KEY_n/VALUE_n
# (git's env form of `-c`), which outranks every config file and is inherited by
# every child process. These tests pin the three properties that matter: the
# EFFECTIVE value is false for the process and its children, NO config file is
# ever written (so there is nothing to restore and no drift to leave behind), and
# the caller's own GIT_CONFIG_* entries survive.
#
# The `_ul_disable_fsmonitor` unit tests only ever invoke `git config`, never
# `git status` (the ul_setup tests do reach the clean-tree gate's `git status`,
# which is safe only because the env override is already in effect): an index refresh with
# fsmonitor live spawns git's native daemon, which is wedged on some setups (bead
# pg2-mgcv5) and would hang the run. The shared setup() gets a fresh empty HOME from the harness and pins
# GIT_CONFIG_SYSTEM to /dev/null (beads pg2-klyn6, pg2-blc3y); a test that needs a global
# value OPTS IN by repointing GIT_CONFIG_GLOBAL at a temp file of its own, never
# at the real ~/.gitconfig.

@test "_ul_disable_fsmonitor overrides a repo-local core.fsmonitor without touching the config file" {
  git config core.fsmonitor true
  [ "$(git config --type=bool --get core.fsmonitor)" = "true" ]

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  # Effective value: off, for this process.
  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
  # ...but the repo's own setting is exactly as it was: nothing was written.
  [ "$(git config --local --get core.fsmonitor)" = "true" ]
}

@test "_ul_disable_fsmonitor overrides a global core.fsmonitor and writes no local key" {
  local global_cfg="$STATE_DIR/gitconfig"
  printf '[core]\n\tfsmonitor = true\n' > "$global_cfg"
  export GIT_CONFIG_GLOBAL="$global_cfg"
  [ "$(git config --type=bool --get core.fsmonitor)" = "true" ]

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
  # A value inherited from global config must never be pinned into the repo.
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]
  # The global file itself is untouched.
  [ "$(git config --file "$global_cfg" --get core.fsmonitor)" = "true" ]
}

@test "_ul_disable_fsmonitor overrides a non-canonical boolean value" {
  # git accepts yes/on/1 as boolean true and spawns the native daemon for them
  # exactly as for `true`.
  git config core.fsmonitor yes

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
  [ "$(git config --local --get core.fsmonitor)" = "yes" ]
}

@test "_ul_disable_fsmonitor overrides a hook-path fsmonitor too" {
  # A hook-path fsmonitor spawns no native daemon, so forcing it off for one
  # update run is harmless; the repo's configured hook path is left in place.
  local hook="/path/to/fsmonitor-watchman.sample"
  git config core.fsmonitor "$hook"

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$(git config --get core.fsmonitor)" = "false" ]
  [ "$(git config --local --get core.fsmonitor)" = "$hook" ]
}

@test "_ul_disable_fsmonitor invents no local key for a repo that never had fsmonitor on" {
  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]
}

@test "_ul_disable_fsmonitor is inherited by child processes" {
  git config core.fsmonitor true

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  # `bash -c` is a fresh process: only the exported environment reaches it. This
  # is what makes the override cover nix, pg-hooks and the update steps.
  run bash -c 'git config --type=bool --get core.fsmonitor'
  [ "$status" -eq 0 ]
  [ "$output" = "false" ]
}

@test "_ul_disable_fsmonitor appends after the caller's own GIT_CONFIG entries" {
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0=user.name
  export GIT_CONFIG_VALUE_0=zed
  git config core.fsmonitor true

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$GIT_CONFIG_COUNT" = "2" ]
  # The caller's entry is intact and still effective...
  [ "$GIT_CONFIG_KEY_0" = "user.name" ]
  [ "$(git config --get user.name)" = "zed" ]
  # ...and ours was added after it.
  [ "$GIT_CONFIG_KEY_1" = "core.fsmonitor" ]
  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
}

@test "_ul_disable_fsmonitor treats a non-numeric GIT_CONFIG_COUNT as 0" {
  # Set the repo value BEFORE exporting the bogus count: git itself rejects a
  # non-numeric GIT_CONFIG_COUNT, so any git call after it fails.
  git config core.fsmonitor true
  export GIT_CONFIG_COUNT=bogus

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$GIT_CONFIG_COUNT" = "1" ]
  [ "$GIT_CONFIG_KEY_0" = "core.fsmonitor" ]
  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
}

@test "_ul_disable_fsmonitor treats a zero-padded GIT_CONFIG_COUNT as base 10" {
  # git parses the count as decimal; bash arithmetic would read "08" as invalid
  # octal and abort.
  git config core.fsmonitor true
  export GIT_CONFIG_COUNT=08
  export GIT_CONFIG_KEY_0=user.name GIT_CONFIG_VALUE_0=a
  export GIT_CONFIG_KEY_1=user.email GIT_CONFIG_VALUE_1=a@b
  export GIT_CONFIG_KEY_2=a.b GIT_CONFIG_VALUE_2=c GIT_CONFIG_KEY_3=a.c GIT_CONFIG_VALUE_3=c
  export GIT_CONFIG_KEY_4=a.d GIT_CONFIG_VALUE_4=c GIT_CONFIG_KEY_5=a.e GIT_CONFIG_VALUE_5=c
  export GIT_CONFIG_KEY_6=a.f GIT_CONFIG_VALUE_6=c GIT_CONFIG_KEY_7=a.g GIT_CONFIG_VALUE_7=c

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$GIT_CONFIG_COUNT" = "9" ]
  [ "$GIT_CONFIG_KEY_8" = "core.fsmonitor" ]
  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
}

# `git fsmonitor--daemon stop` must run exactly when fsmonitor WAS active, which
# also pins the probe-before-export order: exporting the override first would make
# the probe read false and the daemon would never be stopped. A PATH shim records
# the call and answers it, so no real daemon is touched; everything else is
# delegated to the real git.
_install_daemon_stop_shim() {
  local real_git
  real_git="$(command -v git)"
  DAEMON_STOP_LOG="$STATE_DIR/daemon-stop.log"
  : > "$DAEMON_STOP_LOG"
  cat > "$MOCK_BIN/git" <<SHIM
#!/usr/bin/env bash
if [[ "\$1" == "fsmonitor--daemon" && "\$2" == "stop" ]]; then
  echo stop >> "$DAEMON_STOP_LOG"
  exit 0
fi
exec "$real_git" "\$@"
SHIM
  _fix_mock_shebang "$MOCK_BIN/git"
  chmod +x "$MOCK_BIN/git"
  # bash caches the resolved path of `git` from earlier in the test (setup and
  # the repo init); without this the new shim is never found.
  hash -r
}

@test "_ul_disable_fsmonitor stops the running daemon when fsmonitor was active" {
  _install_daemon_stop_shim
  git config core.fsmonitor true

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ "$(wc -l < "$DAEMON_STOP_LOG" | tr -d ' ')" = "1" ]
}

@test "_ul_disable_fsmonitor does not stop a daemon when fsmonitor was not active" {
  _install_daemon_stop_shim

  source "$UL_LOCKS_LIB"
  _ul_disable_fsmonitor

  [ ! -s "$DAEMON_STOP_LOG" ]
}

@test "_ul_disable_fsmonitor removes a stale socket even when fsmonitor is disabled" {
  # A socket left behind by an earlier crashed run makes `nix flake` import fail
  # with "unsupported type" regardless of the current config, so its removal must
  # NOT be gated on fsmonitor having been active.
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

  # Wiring check: ul_setup disabled fsmonitor before reaching its clean-tree gate,
  # and did so without writing a local key.
  [ "$(git config --type=bool --get core.fsmonitor)" = "false" ]
  run git config --local --get core.fsmonitor
  [ "$status" -ne 0 ]

  # Disarm the armed cleanup trap: _ul_cleanup runs `git status`, and tearing down
  # is not what this test is about. The opted-in global config needs no reset —
  # bats runs each test in its own process, so this export cannot leak.
  trap - EXIT INT TERM
}

@test "ul_run_step leaves a repo-local core.fsmonitor untouched after a signal" {
  # The old dance rewrote core.fsmonitor to false and relied on a trap to put it
  # back. The env-only disable never writes it, so the repo's own value survives a
  # killed run by construction.
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
  # MID-RUN: ul_setup has already disabled fsmonitor and the step is running. A
  # config-writing implementation would show "false" here; checking only after the
  # signal cannot tell it from a write-then-restore one.
  cd "$TEST_DIR"
  [ "$(git config --local --get core.fsmonitor)" = "true" ]
  kill -TERM "$script_pid"
  wait "$script_pid" 2>/dev/null || true

  [ "$(git config --local --get core.fsmonitor)" = "true" ]
}

# --- harness hermeticity guard ---

@test "fixture neutralises an ambient global core.fsmonitor (pg2-klyn6/pg2-7hr6o guard, harness-backed)" {
  # The hermeticity itself (fresh empty HOME, GIT_CONFIG_SYSTEM=/dev/null, the
  # allowlist env rebuild) is owned and tested by git-fixture-harness.bash
  # (lib/tests/test-git-fixture-harness-lib.bats). This keeps ONE suite-level
  # assertion of the property that motivated it here: an ambient global
  # core.fsmonitor=true spawns git's native fsmonitor daemon, wedged on some
  # setups (bead pg2-mgcv5), and would hang the run.
  #
  # CONFIG READ ONLY — never `git status`, so the assertion cannot itself spawn a
  # daemon. `--default false` so an unset key reads back as "false".
  [ "$(git config --default false --type=bool --get core.fsmonitor)" = "false" ]
  [ "${GIT_CONFIG_SYSTEM:-}" = "/dev/null" ]
  # HOME is the harness's fresh, empty directory: nothing of the developer's is
  # reachable through it, and it is not the repo under test.
  [ "$HOME" = "$GFH_WORK/home" ]
  [ -z "$(ls -A "$HOME")" ]
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

# --- nix telemetry: nix runs under pg-nix-log-wrapped (pg2-2i29w) ---
#
# A mock wrapper stands in for pg-nix-log-wrapped: it logs its own argv to
# $WRAP_LOG (outside the git tree, so `git add -A` in a step never sweeps it
# up), drops everything up to and including `--`, and execs the rest, which is
# the real wrapper's contract.

_install_mock_wrapper() { # <path>
  WRAP_LOG="$GFH_WORK/wrapper.log"
  cat > "$1" <<'MOCK'
#!/usr/bin/env bash
echo "wrapper: $*" >> "$WRAP_LOG"
while [[ $# -gt 0 && $1 != -- ]]; do shift; done
shift
exec "$@"
MOCK
  _fix_mock_shebang "$1"
  chmod +x "$1"
  export WRAP_LOG
}

# Exports PATH as $MOCK_BIN plus every inherited entry that does NOT hold a
# real pg-nix-log-wrapped (this machine has one). Filtering rather than
# hardcoding "/usr/bin:/bin" keeps coreutils reachable on NixOS, where those
# directories are near-empty.
_path_without_real_wrapper() {
  local dir new="$MOCK_BIN"
  local -a dirs
  IFS=: read -ra dirs <<<"$PATH"
  for dir in "${dirs[@]}"; do
    [[ -n $dir && ! -e $dir/pg-nix-log-wrapped ]] && new+=":$dir"
  done
  export PATH="$new"
}

_recording_nix_mock() {
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "nix: $*" >> "$WRAP_LOG"
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
}

@test "_ul_nix_wrap_prefix is empty without TRACEPARENT even when a wrapper exists" {
  _install_mock_wrapper "$MOCK_BIN/pg-nix-log-wrapped"
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/pg-nix-log-wrapped"
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${#_UL_NIX_PREFIX[@]}" -eq 0 ]
}

@test "_ul_nix_wrap_prefix uses PN_NIX_LOG_WRAPPER and the endpoint pn resolved" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  export PN_NIX_LOG_OTLP_ENDPOINT=http://127.0.0.1:4318
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${_UL_NIX_PREFIX[*]}" = "$MOCK_BIN/my-wrapper --otlp-endpoint http://127.0.0.1:4318 --" ]
}

@test "_ul_nix_wrap_prefix omits --otlp-endpoint when pn gave none" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${_UL_NIX_PREFIX[*]}" = "$MOCK_BIN/my-wrapper --" ]
}

@test "_ul_nix_wrap_prefix falls back to pg-nix-log-wrapped on PATH" {
  _install_mock_wrapper "$MOCK_BIN/pg-nix-log-wrapped"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/does-not-exist"
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${_UL_NIX_PREFIX[*]}" = "$MOCK_BIN/pg-nix-log-wrapped --" ]
}

@test "_ul_nix_wrap_prefix is empty when no wrapper is found" {
  # no real pg-nix-log-wrapped (this machine has one) may be found on PATH
  _path_without_real_wrapper
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/does-not-exist"
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${#_UL_NIX_PREFIX[@]}" -eq 0 ]
}

@test "_ul_nix_wrap_prefix is empty when the wrapper path is not executable" {
  echo "not a program" > "$MOCK_BIN/not-exec"
  # no real pg-nix-log-wrapped (this machine has one) may be found on PATH
  _path_without_real_wrapper
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/not-exec"
  source "$UL_LOCKS_LIB"
  _ul_nix_wrap_prefix
  [ "${#_UL_NIX_PREFIX[@]}" -eq 0 ]
}

@test "_ul_nix_wrap_prefix is empty when telemetry is forced off" {
  _install_mock_wrapper "$MOCK_BIN/pg-nix-log-wrapped"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/pg-nix-log-wrapped"
  source "$UL_LOCKS_LIB"
  PG_NIX_LOG_DISABLE=1 _ul_nix_wrap_prefix
  [ "${#_UL_NIX_PREFIX[@]}" -eq 0 ]
  OTEL_SDK_DISABLED=true _ul_nix_wrap_prefix
  [ "${#_UL_NIX_PREFIX[@]}" -eq 0 ]
}

@test "ul_run_step runs a nix step under the wrapper when telemetry is on" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  _recording_nix_mock
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  export PN_NIX_LOG_OTLP_ENDPOINT=http://127.0.0.1:4318
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  : > "$WRAP_LOG" # ignore ul_setup's own nix calls

  ul_run_step "nix-flake-update" "update-locks: flake" nix flake update --flag "two words"

  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
  grep -qxF "wrapper: --otlp-endpoint http://127.0.0.1:4318 -- nix flake update --flag two words" "$WRAP_LOG"
  grep -qxF "nix: flake update --flag two words" "$WRAP_LOG"
}

@test "ul_run_step leaves a nix step bare when telemetry is off" {
  _install_mock_wrapper "$MOCK_BIN/pg-nix-log-wrapped"
  _recording_nix_mock
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/pg-nix-log-wrapped" # present, but no TRACEPARENT
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  : > "$WRAP_LOG"

  ul_run_step "nix-flake-update" "update-locks: flake" nix flake update

  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
  [ "$(grep -c '^wrapper:' "$WRAP_LOG" || true)" -eq 0 ]
  [ "$(grep -c '^nix: flake update$' "$WRAP_LOG")" -eq 1 ]
}

@test "ul_run_step never wraps a step that is not a nix command" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  _recording_nix_mock
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  source "$UL_LOCKS_LIB"
  ul_setup "test-project" "$TEST_DIR"
  : > "$WRAP_LOG"
  other_step() { echo "other: $*" >> "$WRAP_LOG"; }

  ul_run_step "other-step" "update-locks: other" other_step nix

  [ "$_UL_STEPS_SUCCEEDED" -eq 1 ]
  grep -qxF "other: nix" "$WRAP_LOG"
  [ "$(grep -c '^wrapper:' "$WRAP_LOG" || true)" -eq 0 ]
}

@test "ul_nix runs bare nix with the same argv when no wrapper applies" {
  _install_mock_wrapper "$MOCK_BIN/pg-nix-log-wrapped"
  _recording_nix_mock
  source "$UL_LOCKS_LIB"
  run ul_nix build .#x --no-link
  [ "$status" -eq 0 ]
  [ "$(cat "$WRAP_LOG")" = "nix: build .#x --no-link" ]
}

@test "ul_nix propagates the wrapped command's exit status" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "boom" >&2
exit 7
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  source "$UL_LOCKS_LIB"
  run ul_nix build .#x
  [ "$status" -eq 7 ]
  [[ $output == *boom* ]]
}

@test "_ul_ensure_pre_commit_hooks builds and installs the hook bundle under the wrapper" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  _recording_nix_mock
  export UL_TEST_PG_HOOKS_STATE=stale
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  grep -qxF "wrapper: -- nix build .#install-pre-commit-hooks --no-link" "$WRAP_LOG"
  grep -qxF "wrapper: -- nix run .#install-pre-commit-hooks" "$WRAP_LOG"
}

@test "_ul_ensure_pre_commit_hooks still reads the attr-missing error through the wrapper" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "error: flake does not provide attribute 'packages.x.install-pre-commit-hooks'" >&2
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"
  source "$UL_LOCKS_LIB"
  cd "$TEST_DIR" || return 1

  run _ul_ensure_pre_commit_hooks
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "ul_reexec_in_dev_shell enters the dev shell under the wrapper and keeps the sentinel contract" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  echo "nix: $*" >> "$WRAP_LOG"
  rm -f "$UL_DEVSHELL_SENTINEL"
  echo "ENTERED"
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

  run env -u IN_NIX_SHELL TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01 \
    PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper" "$TEST_DIR/wrap-test.sh" arg1
  [ "$status" -eq 0 ]
  [[ $output == *ENTERED* ]]
  [[ $output != *WARNING* ]]
  [[ $output != *FALLTHROUGH* ]]
  grep -q '^wrapper: -- nix develop ' "$WRAP_LOG"
}

@test "ul_reexec_in_dev_shell falls back to host tools when the wrapped dev shell cannot start" {
  _install_mock_wrapper "$MOCK_BIN/my-wrapper"
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
echo "nix: broken flake" >&2
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  export TRACEPARENT=00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01
  export PN_NIX_LOG_WRAPPER="$MOCK_BIN/my-wrapper"

  run bash -c "
    unset IN_NIX_SHELL
    source '$UL_LOCKS_LIB'
    ul_reexec_in_dev_shell
    echo POST_CALL
  "
  [ "$status" -eq 0 ]
  [[ $output == *WARNING* ]]
  [[ $output == *POST_CALL* ]]
}
