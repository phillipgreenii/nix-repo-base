#!/usr/bin/env bats

# Tests for pn-workspace-push script

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

@test "pn-workspace-push shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-push.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-push shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-push.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-push iterates all workspace projects" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Push repo-base"
    echo "$output" | grep -q "Push terminal-flake"
}

@test "pn-workspace-push runs git push in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: git push"
}

@test "pn-workspace-push fails without workspace root" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--workspace flag uses specified directory" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--root flag works as alias for --workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
}

@test "PN_WORKSPACE_ROOT env resolves workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_ROOT='$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--workspace emits deprecation notice" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    " 2>&1
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "deprecated"
}

@test "--root and --workspace together is an error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace' --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown override-path key errors" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'bogus=/tmp'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    " 2>&1
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'unknown project'
}

@test "pn-workspace-push skips push when project has no remote" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_REMOTE=1
      export MOCK_GIT_BRANCH=feature-branch
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping push for repo-base"
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping push for terminal-flake"
    [[ ! "$output" == *"Mock: git push"* ]]
}

@test "pn-workspace-push skips push when branch has no tracking branch" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=local-only
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping push"
    [[ ! "$output" == *"Mock: git push"* ]]
}

@test "pn-workspace-push reports DETACHED HEAD when current branch is empty" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-push.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'DETACHED HEAD' — skipping push"
}
