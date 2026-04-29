#!/usr/bin/env bats

# Tests for pn-workspace-rebase script

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

@test "pn-workspace-rebase shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-rebase.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-rebase shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-rebase.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-rebase iterates all workspace projects" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Rebase repo-base"
    echo "$output" | grep -q "Rebase terminal-flake"
}

@test "pn-workspace-rebase runs git mu in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: git mu"
}

@test "pn-workspace-rebase fails without workspace root" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--workspace flag uses specified directory" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--root flag works as alias for --workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
}

@test "PN_WORKSPACE_ROOT env resolves workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_ROOT='$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--workspace emits deprecation notice" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    " 2>&1
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "deprecated"
}

@test "--root and --workspace together is an error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace' --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--override-path runs against the swapped path" {
    mkdir -p "$TEST_DIR/wt-base"
    touch "$TEST_DIR/wt-base/flake.nix"
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'repo-base=$TEST_DIR/wt-base'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown override-path key errors" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'bogus=/tmp'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-rebase.sh'
    " 2>&1
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'unknown project'
}
