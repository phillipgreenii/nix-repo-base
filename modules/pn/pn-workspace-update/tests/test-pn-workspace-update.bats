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

@test "--root flag works as alias for --workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
}

@test "PN_WORKSPACE_ROOT env resolves workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_ROOT='$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--workspace emits deprecation notice" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    " 2>&1
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "deprecated"
}

@test "--root and --workspace together is an error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace' --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--override-path runs against the swapped path" {
    mkdir -p "$TEST_DIR/wt-base"
    touch "$TEST_DIR/wt-base/flake.nix"
    cat >"$TEST_DIR/wt-base/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
echo "Mock: update-locks.sh ran in $(basename "$PWD")"
exit 0
EOF
    chmod +x "$TEST_DIR/wt-base/update-locks.sh"
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'repo-base=$TEST_DIR/wt-base'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
}

@test "unknown override-path key errors" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --override-path 'bogus=/tmp'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    " 2>&1
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'unknown project'
}

@test "pn-workspace-update skips pull/push when project has no remote" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_REMOTE=1
      export MOCK_GIT_BRANCH=feature-branch
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping pull/push for repo-base"
    echo "$output" | grep -q "no upstream for branch 'feature-branch' — skipping pull/push for terminal-flake"
    echo "$output" | grep -q "update-locks.sh ran"
    [[ ! "$output" == *"Mock: git pull"* ]]
    [[ ! "$output" == *"Mock: git push"* ]]
}

@test "pn-workspace-update skips pull/push when branch has no tracking branch" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export MOCK_GIT_NO_UPSTREAM=1
      export MOCK_GIT_BRANCH=local-only
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-update.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping pull/push for repo-base"
    echo "$output" | grep -q "no upstream for branch 'local-only' — skipping pull/push for terminal-flake"
    echo "$output" | grep -q "update-locks.sh ran"
    [[ ! "$output" == *"Mock: git pull"* ]]
    [[ ! "$output" == *"Mock: git push"* ]]
}

@test "pn-workspace-update continues past a failing project and reports it" {
  cat >"$TEST_DIR/workspace/repo-base/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
echo "Mock: repo-base update-locks.sh failed"
exit 1
EOF
  chmod +x "$TEST_DIR/workspace/repo-base/update-locks.sh"

  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  echo "$output" | grep -q "Update repo-base"
  echo "$output" | grep -q "Update terminal-flake"
  echo "$output" | grep -q "Failed projects"
  echo "$output" | grep -q "repo-base"
}

@test "pn-workspace-update regenerates workspace lock even when a project fails" {
  cat >"$TEST_DIR/workspace/repo-base/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/workspace/repo-base/update-locks.sh"

  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  [ -f "$TEST_DIR/workspace/pn-workspace.lock" ]
  echo "$output" | grep -q "Regenerating workspace lock"
}

@test "pn-workspace-update exits 0 when all projects succeed" {
  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "All projects updated successfully"
  ! echo "$output" | grep -q "Failed projects"
}

@test "pn-workspace-update skips a dirty repo without running pull/update-locks/push" {
  # repo-base is dirty; terminal-flake is clean and should still run.
  run bash -c "
    source '${LIB_PATH%%:*}'
    export MOCK_GIT_DIRTY_REPOS=repo-base
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  echo "$output" | grep -q "skipping repo-base — working tree has uncommitted changes"
  echo "$output" | grep -q "Skipped projects"
  echo "$output" | grep -q "⊘ repo-base"
  # repo-base's update-locks.sh must NOT have run (terminal-flake's still should)
  ! (echo "$output" | grep -q "update-locks.sh ran in repo-base") || false
  echo "$output" | grep -q "update-locks.sh ran in terminal-flake"
  # The Failed section must not appear when the only problem is dirty-skip
  ! (echo "$output" | grep -q "Failed projects") || false
}

@test "pn-workspace-update skips git pull for a dirty repo (no autostash dance)" {
  run bash -c "
    source '${LIB_PATH%%:*}'
    export MOCK_GIT_DIRTY_REPOS=repo-base
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  # No "Mock: git pull" line for the dirty repo (mock would echo it if invoked).
  # terminal-flake should still see one.
  pull_count=$(echo "$output" | grep -c "Mock: git pull" || true)
  [ "$pull_count" -eq 1 ]
}

@test "pn-workspace-update reports both Skipped and Failed when both occur" {
  cat >"$TEST_DIR/workspace/terminal-flake/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/workspace/terminal-flake/update-locks.sh"

  run bash -c "
    source '${LIB_PATH%%:*}'
    export MOCK_GIT_DIRTY_REPOS=repo-base
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  echo "$output" | grep -q "Skipped projects"
  echo "$output" | grep -q "⊘ repo-base"
  echo "$output" | grep -q "Failed projects"
  echo "$output" | grep -q "✗ terminal-flake"
}
