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

@test "--workspace flag runs from specified directory" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--root flag works as alias for --workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
}

@test "PN_WORKSPACE_ROOT env resolves workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_ROOT='$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--workspace emits deprecation notice" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    " 2>&1
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "deprecated"
}

@test "--root and --workspace together is an error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace' --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--override-path swaps non-terminal repo path" {
    mkdir -p "$TEST_DIR/wt-base"
    touch "$TEST_DIR/wt-base/flake.nix"
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'repo-base=$TEST_DIR/wt-base'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q -- "--override-input repo-base git+file://$TEST_DIR/wt-base"
}

@test "--override-path swaps terminal flake path" {
    mkdir -p "$TEST_DIR/wt-terminal"
    touch "$TEST_DIR/wt-terminal/flake.nix"
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'terminal-flake=$TEST_DIR/wt-terminal'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "$TEST_DIR/wt-terminal"
}

@test "PN_WORKSPACE_OVERRIDE_PATHS env honored" {
    mkdir -p "$TEST_DIR/wt-base"
    touch "$TEST_DIR/wt-base/flake.nix"
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_OVERRIDE_PATHS='repo-base=$TEST_DIR/wt-base'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q -- "--override-input repo-base git+file://$TEST_DIR/wt-base"
}

@test "--override-path flag wins over PN_WORKSPACE_OVERRIDE_PATHS env" {
    mkdir -p "$TEST_DIR/wt-env" "$TEST_DIR/wt-flag"
    touch "$TEST_DIR/wt-env/flake.nix" "$TEST_DIR/wt-flag/flake.nix"
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_OVERRIDE_PATHS='repo-base=$TEST_DIR/wt-env'
      set -- --override-path 'repo-base=$TEST_DIR/wt-flag'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "git+file://$TEST_DIR/wt-flag"
    ! echo "$output" | grep -q "git+file://$TEST_DIR/wt-env"
}

@test "unknown override-path key errors" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'bogus=/tmp'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    " 2>&1
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'unknown project'
}

@test "--terminal-path is no longer accepted" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --terminal-path '$TEST_DIR/workspace/terminal-flake'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -ne 0 ]
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-build.sh'
    "
    [ "$status" -ne 0 ]
}
