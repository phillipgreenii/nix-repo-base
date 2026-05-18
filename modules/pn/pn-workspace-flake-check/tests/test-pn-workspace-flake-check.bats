#!/usr/bin/env bats

# Tests for pn-workspace-flake-check script

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

LIB_PATH="${LIB_PATH:-$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash}"

run_script() {
  # shellcheck disable=SC1090
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-workspace-flake-check.sh'" -- "$@"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace
    create_mock_pn_discover_workspace

    # cd into the workspace so workspace_resolve_root finds pn-workspace.toml
    cd "$TEST_DIR/workspace"

    # Mock pn-ws-nix that always succeeds, recording its CWD and args.
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
echo "Mock pn-ws-nix in $PWD called with: $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "runs pn-ws-nix flake check in each workspace project" {
    run run_script
    [ "$status" -eq 0 ]
    # Mock workspace should have multiple projects; expect at least 1 invocation
    count=$(echo "$output" | grep -c "called with: flake check" || true)
    [ "$count" -ge 1 ]
}

@test "exits non-zero when one project fails" {
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
case "$PWD" in
  *terminal-flake*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    run run_script
    [ "$status" -ne 0 ]
}

@test "full sweep: visits every project despite an early failure" {
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
echo "ran in $PWD"
case "$PWD" in
  *repo-base*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    run run_script
    [ "$status" -ne 0 ]
    # All projects in the mock workspace should be visited; expect >= 2
    invocations=$(echo "$output" | grep -c "ran in" || true)
    [ "$invocations" -ge 2 ]
}

@test "summary line lists failing project names" {
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
case "$PWD" in
  *terminal-flake*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    run run_script
    [ "$status" -ne 0 ]
    [[ "$output" == *"terminal-flake"* ]]
    [[ "$output" == *"FAIL"* || "$output" == *"fail"* ]]
}

@test "OK summary when all projects pass" {
    run run_script
    [ "$status" -eq 0 ]
    [[ "$output" == *"OK"* || "$output" == *"passed"* ]]
}

@test "--help exits 0 with usage text" {
    run run_script --help
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-workspace-flake-check"* ]]
    [[ "$output" == *"--root"* ]]
}

@test "--root flag is honored" {
    run run_script --root="$TEST_DIR/workspace"
    [ "$status" -eq 0 ]
}

@test "rejects unknown options" {
    run run_script --no-such-flag
    [ "$status" -ne 0 ]
}
