#!/usr/bin/env bats

# Tests for pn-workspace-update script

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
    create_mock_git
    create_mock_pn_discover_workspace

    # Create stub update-locks.sh in each workspace repo
    for repo in repo-base terminal-flake; do
      cat >"$TEST_DIR/workspace/$repo/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
echo "Mock: update-locks.sh ran in $(basename "$PWD")"
exit 0
EOF
      chmod +x "$TEST_DIR/workspace/$repo/update-locks.sh"
    done

    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-update shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-update.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-update shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-update.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-update exits with error when not in workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -ne 0 ]
    echo "$output" | grep -qi "pn-workspace.toml"
}

@test "pn-workspace-update iterates all workspace projects" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Update repo-base"
    echo "$output" | grep -q "Update terminal-flake"
}

@test "pn-workspace-update runs git pull in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: git pull"
}

@test "pn-workspace-update runs update-locks.sh in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "update-locks.sh ran"
}

@test "pn-workspace-update runs git push in each project" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Mock: git push"
}

@test "pn-workspace-update regenerates workspace lock file after updating" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    [ -f "$TEST_DIR/workspace/pn-workspace.lock" ]
    echo "$output" | grep -q "Regenerating workspace lock"
}

@test "pn-workspace-update exits 143 on SIGTERM during git pull" {
    local ready_fifo="$TEST_DIR/git-ready"
    mkfifo "$ready_fifo"

    cat >"$TEST_DIR/git" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == "pull" ]]; then
  echo ready > "$ready_fifo"
  sleep 60
fi
echo "Mock: git \$*"
exit 0
EOF
    chmod +x "$TEST_DIR/git"

    bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    " &
    local pid=$!
    read -r < "$ready_fifo"
    kill -TERM $pid
    local rc=0
    wait $pid 2>/dev/null || rc=$?

    # 143 = 128 + 15 (SIGTERM)
    [ "$rc" -eq 143 ]
}

@test "--workspace flag uses specified directory" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -ne 0 ]
}
