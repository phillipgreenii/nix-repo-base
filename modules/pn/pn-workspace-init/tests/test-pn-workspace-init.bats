#!/usr/bin/env bats

# Tests for pn-workspace-init script

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

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    create_mock_pn_discover_workspace
    mkdir -p "$TEST_DIR/target"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-init shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-init shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-init creates pn-workspace.toml in target directory" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    [ -f "$TEST_DIR/target/pn-workspace.toml" ]
}

@test "pn-workspace-init toml contains apply_command" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    grep -q "apply_command" "$TEST_DIR/target/pn-workspace.toml"
    grep -q "darwin-rebuild" "$TEST_DIR/target/pn-workspace.toml"
}

@test "pn-workspace-init toml contains post_apply_hooks with pn-osx-tcc-check" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    grep -q "pn-osx-tcc-check" "$TEST_DIR/target/pn-workspace.toml"
}

@test "pn-workspace-init toml contains use_lock = true" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    grep -q "use_lock = true" "$TEST_DIR/target/pn-workspace.toml"
}

@test "pn-workspace-init defaults to CWD when no directory argument given" {
    run bash -c "cd '$TEST_DIR/target' && bash '$SCRIPTS_DIR/pn-workspace-init.sh'"
    [ "$status" -eq 0 ]
    [ -f "$TEST_DIR/target/pn-workspace.toml" ]
}

@test "pn-workspace-init fails if pn-workspace.toml already exists without --force" {
    touch "$TEST_DIR/target/pn-workspace.toml"
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "already exists"
}

@test "pn-workspace-init succeeds with --force when file already exists" {
    touch "$TEST_DIR/target/pn-workspace.toml"
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" --force "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    grep -q "apply_command" "$TEST_DIR/target/pn-workspace.toml"
}

@test "pn-workspace-init succeeds with -f when file already exists" {
    touch "$TEST_DIR/target/pn-workspace.toml"
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" -f "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    grep -q "apply_command" "$TEST_DIR/target/pn-workspace.toml"
}

@test "pn-workspace-init generates lock file via pn-discover-workspace" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    [ -f "$TEST_DIR/target/pn-workspace.lock" ]
}

@test "pn-workspace-init skips lock file with warning when pn-discover-workspace not found" {
    # Remove pn-discover-workspace from PATH by using a PATH without TEST_DIR
    run env PATH="/usr/bin:/bin" bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target"
    [ "$status" -eq 0 ]
    [ ! -f "$TEST_DIR/target/pn-workspace.lock" ]
    echo "$output" | grep -q "Warning"
}

@test "pn-workspace-init fails with unknown option" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" --unknown
    [ "$status" -ne 0 ]
}

@test "pn-workspace-init fails with extra positional arguments" {
    run bash "$SCRIPTS_DIR/pn-workspace-init.sh" "$TEST_DIR/target" "$TEST_DIR/extra"
    [ "$status" -ne 0 ]
}
