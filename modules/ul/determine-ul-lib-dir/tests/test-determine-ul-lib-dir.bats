#!/usr/bin/env bats

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

SCRIPT="$SCRIPTS_DIR/determine-ul-lib-dir.sh"

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

_make_workspace_with_sibling() {
  local ws="$1"
  mkdir -p "$ws/phillipg-nix-repo-base/lib/scripts"
  : >"$ws/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
}

@test "UL_LIB_DIR_OVERRIDE takes precedence over everything" {
  export UL_LIB_DIR_OVERRIDE="/override/path"
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "/override/path" ]
}

@test "WORKSPACE_ROOT + sibling-on-disk wins over baked nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/phillipg-nix-repo-base/lib/scripts" ]
}

@test "UL_IGNORE_WORKSPACE_ROOT skips the sibling check even when sibling exists" {
  export WORKSPACE_ROOT="$TEST_DIR"
  export UL_IGNORE_WORKSPACE_ROOT=1
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "WORKSPACE_ROOT set but sibling missing falls back to nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  [ ! -f "$WORKSPACE_ROOT/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash" ]

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
