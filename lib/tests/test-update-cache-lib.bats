#!/usr/bin/env bats
# shellcheck disable=SC1090

SCRIPT_DIR="$BATS_TEST_DIRNAME"
if [[ -n ${UL_LIB_SCRIPTS_DIR:-} ]]; then
  UL_LIB_SCRIPT="$UL_LIB_SCRIPTS_DIR/update-cache-lib.bash"
else
  UL_LIB_SCRIPT="$(cd "$SCRIPT_DIR/../scripts" && pwd)/update-cache-lib.bash"
fi


setup() {
  TEST_DIR=$(mktemp -d)
  export XDG_STATE_HOME="$TEST_DIR/state"
  export NIX_UL_FORCE_UPDATE="false"
  source "$UL_LIB_SCRIPT"
}

# Replace the shebang on $1 with one that uses an absolute bash path.
# Required for environments where /usr/bin/env doesn't exist (e.g. the
# Nix build sandbox, where only /nix/store paths are visible).
_fix_mock_shebang() {
  sed -i "1s|.*|#!$(command -v bash)|" "$1"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "ul_init creates state directory for project" {
  ul_init "my-project"
  [ -d "$XDG_STATE_HOME/zn-self-upgrade/my-project/steps" ]
}

@test "ul_init sets _UL_PROJECT variable" {
  ul_init "my-project"
  [ "$_UL_PROJECT" = "my-project" ]
}

@test "ul_init uses default XDG_STATE_HOME when unset" {
  unset XDG_STATE_HOME
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project"
  [ -d "$TEST_DIR/home/.local/state/zn-self-upgrade/my-project/steps" ]
}

# --- ul_should_run / ul_mark_done ---

@test "ul_should_run returns 0 when marker does not exist" {
  ul_init "my-project"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 1 when marker is fresh" {
  ul_init "my-project"
  ul_mark_done "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 1 ]
}

@test "ul_should_run returns 0 when marker is stale" {
  ul_init "my-project"
  ul_mark_done "some-step"
  local marker="$UL_STATE_DIR/my-project/steps/some-step"
  touch -t 202501010000 "$marker"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_should_run returns 0 when UL_FORCE is true" {
  export NIX_UL_FORCE_UPDATE="true"
  source "$UL_LIB_SCRIPT"
  ul_init "my-project"
  ul_mark_done "some-step"
  run ul_should_run "some-step"
  [ "$status" -eq 0 ]
}

@test "ul_mark_done creates marker file" {
  ul_init "my-project"
  ul_mark_done "some-step"
  [ -f "$UL_STATE_DIR/my-project/steps/some-step" ]
}

@test "ul_mark_done updates marker mtime on repeated calls" {
  ul_init "my-project"
  ul_mark_done "some-step"
  local marker="$UL_STATE_DIR/my-project/steps/some-step"
  touch -t 202501010000 "$marker"
  local old_mtime
  old_mtime=$(stat -c %Y "$marker" 2>/dev/null || stat -f %m "$marker")
  sleep 1
  ul_mark_done "some-step"
  local new_mtime
  new_mtime=$(stat -c %Y "$marker" 2>/dev/null || stat -f %m "$marker")
  [ "$new_mtime" -gt "$old_mtime" ]
}

# --- Skip message formatting ---

@test "ul_should_run skip message includes step name" {
  ul_init "my-project"
  ul_mark_done "brew-update"
  run ul_should_run "brew-update"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "Skipping brew-update" ]]
}

@test "ul_should_run skip message includes last successful timestamp" {
  ul_init "my-project"
  ul_mark_done "brew-update"
  run ul_should_run "brew-update"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "last successful at" ]]
}

@test "ul_should_run skip message includes time remaining" {
  ul_init "my-project"
  ul_mark_done "brew-update"
  run ul_should_run "brew-update"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "next eligible in" ]]
  [[ "$output" =~ [0-9]+h\ [0-9]+m\ [0-9]+s ]]
}

@test "ul_should_run prints nothing when step should run (no marker)" {
  ul_init "my-project"
  run ul_should_run "new-step"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# --- ul_needs_rebuild / ul_mark_applied ---

setup_test_repo() {
  local repo_path="$1"
  mkdir -p "$repo_path"
  cd "$repo_path" || exit
  command git init
  command git config user.email "test@test.com"
  command git config user.name "Test"
  echo "content" > file.txt
  command git add file.txt
  command git commit -m "init"
  cd - > /dev/null || exit
}

@test "ul_needs_rebuild returns 0 when no applied hash exists" {
  setup_test_repo "$TEST_DIR/repo-a"
  run ul_needs_rebuild "$TEST_DIR/repo-a"
  [ "$status" -eq 0 ]
}

@test "ul_needs_rebuild returns 1 when applied hash matches HEAD" {
  setup_test_repo "$TEST_DIR/repo-a"
  ul_mark_applied "$TEST_DIR/repo-a"
  run ul_needs_rebuild "$TEST_DIR/repo-a"
  [ "$status" -eq 1 ]
}

@test "ul_needs_rebuild returns 0 when applied hash differs from HEAD" {
  setup_test_repo "$TEST_DIR/repo-a"
  ul_mark_applied "$TEST_DIR/repo-a"
  cd "$TEST_DIR/repo-a"
  echo "new content" > file.txt
  command git add file.txt
  command git commit -m "change"
  cd - > /dev/null
  run ul_needs_rebuild "$TEST_DIR/repo-a"
  [ "$status" -eq 0 ]
}

@test "ul_needs_rebuild returns 0 if any project differs (multiple projects)" {
  setup_test_repo "$TEST_DIR/repo-a"
  setup_test_repo "$TEST_DIR/repo-b"
  ul_mark_applied "$TEST_DIR/repo-a" "$TEST_DIR/repo-b"
  cd "$TEST_DIR/repo-b"
  echo "new" > file.txt
  command git add file.txt
  command git commit -m "change"
  cd - > /dev/null
  run ul_needs_rebuild "$TEST_DIR/repo-a" "$TEST_DIR/repo-b"
  [ "$status" -eq 0 ]
}

@test "ul_needs_rebuild returns 1 when all projects match (multiple)" {
  setup_test_repo "$TEST_DIR/repo-a"
  setup_test_repo "$TEST_DIR/repo-b"
  ul_mark_applied "$TEST_DIR/repo-a" "$TEST_DIR/repo-b"
  run ul_needs_rebuild "$TEST_DIR/repo-a" "$TEST_DIR/repo-b"
  [ "$status" -eq 1 ]
}

@test "ul_needs_rebuild returns 0 when UL_FORCE is true" {
  export NIX_UL_FORCE_UPDATE="true"
  source "$UL_LIB_SCRIPT"
  setup_test_repo "$TEST_DIR/repo-a"
  ul_mark_applied "$TEST_DIR/repo-a"
  run ul_needs_rebuild "$TEST_DIR/repo-a"
  [ "$status" -eq 0 ]
}

@test "ul_mark_applied writes git hash to applied-hash file" {
  setup_test_repo "$TEST_DIR/repo-a"
  ul_mark_applied "$TEST_DIR/repo-a"
  local expected_hash
  expected_hash=$(cd "$TEST_DIR/repo-a" && command git rev-parse HEAD)
  local stored_hash
  stored_hash=$(cat "$UL_STATE_DIR/apply/applied-hash/repo-a")
  [ "$stored_hash" = "$expected_hash" ]
}

# --- ul_check_nix_daemon ---

@test "ul_check_nix_daemon succeeds silently when nix eval works" {
  # Mock nix that succeeds immediately
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  # Mock timeout that just runs the command
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
shift  # skip timeout value
exec "$@"
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  rm -rf "$MOCK_BIN"
}

@test "ul_check_nix_daemon exits 1 on timeout (non-interactive)" {
  # Mock timeout that simulates timeout exit code 124
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
exit 124
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  # Non-interactive: stdin is not a terminal (bats run already handles this)
  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 1 ]
  [[ "$output" =~ "unresponsive" ]]
}

@test "ul_check_nix_daemon exits 1 on nix failure (non-interactive)" {
  # Mock timeout that passes through to a failing nix
  MOCK_BIN=$(mktemp -d)
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
exit 1
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"
  cat > "$MOCK_BIN/timeout" <<'MOCK'
#!/usr/bin/env bash
shift
exec "$@"
MOCK
  _fix_mock_shebang "$MOCK_BIN/timeout"
  chmod +x "$MOCK_BIN/timeout"

  PATH="$MOCK_BIN:$PATH" run ul_check_nix_daemon
  [ "$status" -eq 1 ]
  [[ "$output" =~ "nix daemon" ]]
  rm -rf "$MOCK_BIN"
}
