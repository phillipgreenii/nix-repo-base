#!/usr/bin/env bats

# Tests for pn-workspace-upgrade script

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

    # Create mock pn-workspace-update
    cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update ran"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-update"

    # Create mock pn-workspace-apply
    cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-apply ran"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-apply"

    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "pn-workspace-upgrade shows help with --help" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-upgrade shows help with -h" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage"
}

@test "pn-workspace-upgrade calls pn-workspace-update" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "pn-workspace-update ran"
}

@test "pn-workspace-upgrade calls pn-workspace-apply" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "pn-workspace-apply ran"
}

@test "pn-workspace-upgrade calls update before apply" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
    [ "$status" -eq 0 ]
    update_line=$(echo "$output" | grep -n "pn-workspace-update ran" | head -1 | cut -d: -f1)
    apply_line=$(echo "$output" | grep -n "pn-workspace-apply ran" | head -1 | cut -d: -f1)
    [[ -n $update_line && -n $apply_line ]]
    [ "$update_line" -lt "$apply_line" ]
}

@test "pn-workspace-upgrade does not call apply when update fails" {
    cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update failed"
exit 1
EOF
    chmod +x "$TEST_DIR/pn-workspace-update"

    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
    [ "$status" -ne 0 ]
    ! (echo "$output" | grep -q "pn-workspace-apply ran") || false
}

@test "--workspace flag is forwarded to pn-workspace-update" {
    cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-update"

    cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-apply $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-apply"

    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" --workspace "$TEST_DIR/workspace"
    [ "$status" -eq 0 ]
    echo "$output" | grep "pn-workspace-update" | grep -q -- "--workspace"
}

@test "--workspace flag is forwarded to pn-workspace-apply" {
    cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-update"

    cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-apply $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-apply"

    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" --workspace "$TEST_DIR/workspace"
    [ "$status" -eq 0 ]
    echo "$output" | grep "pn-workspace-apply" | grep -q -- "--workspace"
}

@test "--apply-cmd flag is forwarded only to pn-workspace-apply" {
    cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-update"

    cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-apply $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-workspace-apply"

    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" --apply-cmd "custom-apply"
    [ "$status" -eq 0 ]
    echo "$output" | grep "pn-workspace-apply" | grep -q -- "--apply-cmd"
    ! (echo "$output" | grep "pn-workspace-update" | grep -q -- "--apply-cmd") || false
}

@test "unknown flag exits with error" {
    run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh" --bogus-flag
    [ "$status" -ne 0 ]
}
