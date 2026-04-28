#!/usr/bin/env bats

# Tests for pn-workspace-check script

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

run_script() {
  # shellcheck disable=SC1090
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-workspace-check.sh'" -- "$@"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace

    create_mock_pn_discover_workspace

    # Create mock pre-commit
    cat >"$TEST_DIR/pre-commit" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pre-commit $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pre-commit"
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-check shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-check.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-check shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-check.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-check iterates all workspace projects" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-check.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Check repo-base"
    echo "$output" | grep -q "Check terminal-flake"
}

@test "pn-workspace-check runs pre-commit in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-check.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: pre-commit run --all-files"
}

@test "pn-workspace-check fails without workspace root" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-check.sh'
    "
    [ "$status" -ne 0 ]
}
