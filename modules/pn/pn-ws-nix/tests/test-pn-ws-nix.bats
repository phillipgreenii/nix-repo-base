#!/usr/bin/env bats

# Tests for pn-ws-nix script

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
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-ws-nix.sh'" -- "$@"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace

    create_mock_pn_discover_workspace
    # Provide non-terminal entries with inputName so override flags get injected
    export PN_DISCOVER_OUTPUT='[{"path":"'"$TEST_DIR"'/workspace/repo-base","inputName":"repo-base-input"},{"path":"'"$TEST_DIR"'/workspace/terminal-flake"}]'

    # cd into the workspace so workspace_resolve_root finds pn-workspace.toml
    cd "$TEST_DIR/workspace"

    # Mock nix binary that prints its args and exits 0
    cat >"$TEST_DIR/nix" <<'EOF'
#!/usr/bin/env bash
echo "Mock nix called with: $*"
exit 0
EOF
    chmod +x "$TEST_DIR/nix"
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    assert_no_real_paths_touched
    rm -rf "$TEST_DIR"
}

@test "passes args through to nix when not in deny-list" {
    run run_script build .#hello
    [ "$status" -eq 0 ]
    [[ "$output" == *"Mock nix called with: build .#hello"* ]]
}

@test "injects --override-input for each workspace project" {
    run run_script eval .#x
    [ "$status" -eq 0 ]
    [[ "$output" == *"--override-input"* ]]
}

@test "flake update triggers warn by default" {
    run run_script flake update
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
    [[ "$output" != *"Mock nix called with"*"--override-input"* ]]
}

@test "flake lock triggers warn by default" {
    run run_script flake lock
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
}

@test "--non-override-subcommand-action=error exits 2 for flake update" {
    run run_script --non-override-subcommand-action=error flake update
    [ "$status" -eq 2 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
}

@test "--non-override-subcommand-action=ignore is silent and runs nix" {
    run run_script --non-override-subcommand-action=ignore flake update
    [ "$status" -eq 0 ]
    [[ "$output" == *"Mock nix called with: flake update"* ]]
    [[ "$output" != *"pn-ws-nix: overrides not applicable"* ]]
}

@test "env var PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION sets action" {
    PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION=ignore run run_script flake update
    [ "$status" -eq 0 ]
    [[ "$output" != *"pn-ws-nix: overrides not applicable"* ]]
}

@test "flag overrides env var" {
    PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION=ignore \
      run run_script --non-override-subcommand-action=error flake update
    [ "$status" -eq 2 ]
}

@test "invalid action value exits 2 with usage" {
    run run_script --non-override-subcommand-action=bogus build .#x
    [ "$status" -eq 2 ]
    [[ "$output" == *"--non-override-subcommand-action"* ]]
}

@test "operates on workspace with no pn-workspace.lock by invoking pn-discover-workspace" {
    # Remove the lock if setup created one; force regeneration via discover.
    rm -f "$TEST_DIR/workspace/pn-workspace.lock"

    # The mock pn-discover-workspace emits PN_DISCOVER_OUTPUT (set in setup)
    # which contains at least one inputName-bearing project, so the script
    # should still emit at least one --override-input.
    run run_script eval .#x
    [ "$status" -eq 0 ]
    [[ "$output" == *"--override-input"* ]]
}

@test "use_lock=false with stale lockfile emits warning (requires yq)" {
    # This test exercises yq's TOML parsing: with use_lock=false and a
    # lockfile present, workspace_get_projects emits a specific warning.
    # If yq is missing from PATH, use_lock silently defaults to "true",
    # the lockfile is used, and no warning is emitted — so this test
    # proves yq is reachable at runtime.
    cat >"$TEST_DIR/workspace/pn-workspace.toml" <<'TOML'
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
pre_apply_hooks = []
post_apply_hooks = []
use_lock = false
TOML
    # Pre-create a stale lockfile (contents don't matter for the warning).
    cat >"$TEST_DIR/workspace/pn-workspace.lock" <<EOF
[{"path":"$TEST_DIR/workspace/repo-base","inputName":"stale-lock-input"}]
EOF

    run run_script eval .#x
    [ "$status" -eq 0 ]
    [[ "$output" == *"warning: pn-workspace.lock exists but use_lock=false"* ]]
    # The script should still produce overrides from pn-discover-workspace,
    # not from the stale lockfile.
    [[ "$output" == *"repo-base-input"* ]]
    [[ "$output" != *"stale-lock-input"* ]]
}

@test "deny-list still triggers when global nix flags precede subcommand" {
    run run_script --verbose flake update
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
    [[ "$output" != *"--override-input"* ]]
}

@test "refuses to append overrides when user args contain --" {
    run run_script run .#tool -- arg1 arg2
    [ "$status" -ne 0 ]
    [[ "$output" == *"--"* ]]
}
