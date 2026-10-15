#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

SCRIPT="$SCRIPTS_DIR/determine-ul-lib-dir.sh"

# The repo directory has two names: the legacy phillipg-nix-repo-base and the
# post-move nix-repo-base. Every sibling-resolution test runs against both.
OLD_NAME="phillipg-nix-repo-base"
NEW_NAME="nix-repo-base"

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR

  # Sentinel default if not set by the nix check derivation.
  export UL_LIB_PACKAGE_PATH="${UL_LIB_PACKAGE_PATH:-/sentinel/nix-store/lib/scripts}"

  unset UL_LIB_DIR_OVERRIDE UL_IGNORE_WORKSPACE_ROOT WORKSPACE_ROOT
}

teardown() {
  rm -rf "$TEST_DIR"
}

# _make_workspace_with_sibling <workspace> <repo-dir-name>
_make_workspace_with_sibling() {
  local ws="$1" name="$2"
  mkdir -p "$ws/$name/lib/scripts"
  : >"$ws/$name/lib/scripts/update-locks-lib.bash"
}

@test "UL_LIB_DIR_OVERRIDE takes precedence over everything (old layout)" {
  export UL_LIB_DIR_OVERRIDE="/override/path"
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$OLD_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "/override/path" ]
}

@test "UL_LIB_DIR_OVERRIDE takes precedence over everything (new layout)" {
  export UL_LIB_DIR_OVERRIDE="/override/path"
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$NEW_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "/override/path" ]
}

@test "WORKSPACE_ROOT + old-name sibling-on-disk wins over baked nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$OLD_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/$OLD_NAME/lib/scripts" ]
}

@test "WORKSPACE_ROOT + new-name sibling-on-disk wins over baked nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$NEW_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/$NEW_NAME/lib/scripts" ]
}

@test "new-name sibling wins when both directory names exist" {
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$OLD_NAME"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$NEW_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/$NEW_NAME/lib/scripts" ]
}

@test "old-name sibling is used when the new-name directory lacks the lib file" {
  export WORKSPACE_ROOT="$TEST_DIR"
  mkdir -p "$WORKSPACE_ROOT/$NEW_NAME/lib/scripts"
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$OLD_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/$OLD_NAME/lib/scripts" ]
}

@test "UL_IGNORE_WORKSPACE_ROOT skips the sibling check even when old-name sibling exists" {
  export WORKSPACE_ROOT="$TEST_DIR"
  export UL_IGNORE_WORKSPACE_ROOT=1
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$OLD_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "UL_IGNORE_WORKSPACE_ROOT skips the sibling check even when new-name sibling exists" {
  export WORKSPACE_ROOT="$TEST_DIR"
  export UL_IGNORE_WORKSPACE_ROOT=1
  _make_workspace_with_sibling "$WORKSPACE_ROOT" "$NEW_NAME"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "WORKSPACE_ROOT set but sibling missing falls back to nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  [ ! -f "$WORKSPACE_ROOT/$OLD_NAME/lib/scripts/update-locks-lib.bash" ]
  [ ! -f "$WORKSPACE_ROOT/$NEW_NAME/lib/scripts/update-locks-lib.bash" ]

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "no WORKSPACE_ROOT falls back to nix-store path" {
  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "non-existent WORKSPACE_ROOT directory does not match" {
  export WORKSPACE_ROOT="/this/path/does/not/exist"
  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}
