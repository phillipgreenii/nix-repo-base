#!/usr/bin/env bats

# Tests for pn-workspace-status script

# Resolve scripts directory
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

# Load test support
if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

# LIB_PATH is set by the nix test runner; fall back to local pn-lib for dev
LIB_PATH="${LIB_PATH:-$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash}"

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace

    create_mock_pn_discover_workspace
    create_mock_git
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-status shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-status.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-status shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-status.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-status iterates all workspace projects" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-status.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Status repo-base"
    echo "$output" | grep -q "Status terminal-flake"
}

@test "pn-workspace-status runs git status in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-status.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: git status"
}

@test "pn-workspace-status fails without workspace root" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-status.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--workspace flag uses specified directory" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-status.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-status.sh'
    "
    [ "$status" -ne 0 ]
}
