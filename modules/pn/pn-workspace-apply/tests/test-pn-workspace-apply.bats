#!/usr/bin/env bats

# Tests for pn-workspace-apply script

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
    create_mock_sudo

    # Provide a mock pn-discover-workspace that returns 2-repo JSON:
    # repo-base has inputName (non-terminal), terminal-flake has none (terminal)
    export PN_DISCOVER_OUTPUT="[{\"path\":\"$TEST_DIR/workspace/repo-base\",\"inputName\":\"repo-base\"},{\"path\":\"$TEST_DIR/workspace/terminal-flake\"}]"
    create_mock_pn_discover_workspace

    # Mock hostname
    cat >"$TEST_DIR/hostname" <<'EOF'
#!/usr/bin/env bash
echo "test-host"
EOF
    chmod +x "$TEST_DIR/hostname"

    # Mock readlink (for profile comparison)
    cat >"$TEST_DIR/readlink" <<'EOF'
#!/usr/bin/env bash
echo "/nix/var/nix/profiles/system-1-link"
EOF
    chmod +x "$TEST_DIR/readlink"

    # Mock nvd
    cat >"$TEST_DIR/nvd" <<'EOF'
#!/usr/bin/env bash
echo "Mock: nvd $*"
exit 0
EOF
    chmod +x "$TEST_DIR/nvd"

    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-apply shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-apply.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-apply shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-apply.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-apply exits with error when not in workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -ne 0 ]
    echo "$output" | grep -qi "pn-workspace.toml"
}

@test "pn-workspace-apply builds --override-input args for non-terminal repos" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q -- "--override-input repo-base"
}

@test "pn-workspace-apply uses git+file:// scheme for overrides" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "git+file://"
}

@test "pn-workspace-apply override path contains workspace repo path" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "$TEST_DIR/workspace/repo-base"
}

@test "pn-workspace-apply does not add --override-input for terminal flake" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    # terminal-flake has no inputName so it must not appear as an override key
    ! (echo "$output" | grep -q -- "--override-input terminal-flake") || false
}

@test "pn-workspace-apply calls apply command with substituted terminal flake path" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    # apply_command template uses {terminal_flake}; should see the resolved path
    echo "$output" | grep -q "$TEST_DIR/workspace/terminal-flake"
}


@test "pn-workspace-apply exits 143 on SIGTERM during apply" {
    # Slow darwin-rebuild mock that signals readiness via a FIFO
    local ready_fifo="$TEST_DIR/dr-ready"
    mkfifo "$ready_fifo"

    cat >"$TEST_DIR/darwin-rebuild" <<EOF
#!/usr/bin/env bash
echo ready > "$ready_fifo"
sleep 60
EOF
    chmod +x "$TEST_DIR/darwin-rebuild"

    bash -c "
      source '${LIB_PATH%%:*}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    " &
    local pid=$!
    # Wait for darwin-rebuild to signal it started
    read -r < "$ready_fifo"
    kill -TERM $pid
    local rc=0
    wait $pid 2>/dev/null || rc=$?

    # 143 = 128 + 15 (SIGTERM)
    [ "$rc" -eq 143 ]
}

@test "--workspace flag overrides CWD-based discovery" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--root flag works as alias for --workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
}

@test "PN_WORKSPACE_ROOT env resolves workspace" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      export PN_WORKSPACE_ROOT='$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
}

@test "--workspace emits deprecation notice" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    " 2>&1
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "deprecated"
}

@test "--root and --workspace together is an error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --root '$TEST_DIR/workspace' --workspace '$TEST_DIR/workspace'
      cd '$TEST_HOME'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
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
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    " 2>&1
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'unknown project'
}

@test "--terminal-path is no longer accepted" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --terminal-path '$TEST_DIR/workspace/terminal-flake'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -ne 0 ]
}

@test "--apply-cmd flag overrides apply_command from TOML" {
    cat >"$TEST_DIR/mock-apply-override" <<'EOF'
#!/usr/bin/env bash
echo "Mock: mock-apply-override $*"
exit 0
EOF
    chmod +x "$TEST_DIR/mock-apply-override"

    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --apply-cmd 'mock-apply-override --flake {terminal_flake}#{hostname}'
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "mock-apply-override"
}

@test "--apply-cmd is required when no pn-workspace.toml and no flag" {
    mkdir -p "$TEST_DIR/empty-workspace"

    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --workspace '$TEST_DIR/empty-workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -ne 0 ]
    echo "$output" | grep -q -- "--apply-cmd"
}

@test "unknown flag exits with error" {
    run bash -c "
      source '${LIB_PATH%%:*}'
      set -- --bogus-flag
      cd '$TEST_DIR/workspace'
      source '$SCRIPTS_DIR/pn-workspace-apply.sh'
    "
    [ "$status" -ne 0 ]
}
