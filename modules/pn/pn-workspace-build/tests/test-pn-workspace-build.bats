#!/usr/bin/env bats

# Tests for pn-workspace-build script

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
    create_mock_nix
    create_mock_darwin_rebuild

    # Provide a mock pn-discover-workspace with inputName on non-terminal repo
    export PN_DISCOVER_OUTPUT="[{\"path\":\"$TEST_DIR/workspace/repo-base\",\"inputName\":\"repo-base\"},{\"path\":\"$TEST_DIR/workspace/terminal-flake\"}]"
    create_mock_pn_discover_workspace

    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-build shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-build.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-build shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-build.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-build exits with error when not in workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -ne 0 ]
    echo "$output" | grep -qi "pn-workspace.toml"
}

@test "pn-workspace-build calls darwin-rebuild build" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "darwin-rebuild build"
}

@test "pn-workspace-build passes --override-input for non-terminal repos" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q -- "--override-input repo-base"
}

@test "pn-workspace-build uses git+file:// scheme for overrides" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "git+file://"
}

@test "pn-workspace-build formats terminal flake before building" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    # nix fmt should appear before darwin-rebuild build in the output
    fmt_line=$(echo "$output" | grep -n "nix fmt" | head -1 | cut -d: -f1)
    build_line=$(echo "$output" | grep -n "darwin-rebuild build" | head -1 | cut -d: -f1)
    [[ -n $fmt_line && -n $build_line ]]
    [ "$fmt_line" -lt "$build_line" ]
}

@test "pn-workspace-build prints success message with apply instructions" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Build successful"
    echo "$output" | grep -q "pn-workspace-apply"
}
