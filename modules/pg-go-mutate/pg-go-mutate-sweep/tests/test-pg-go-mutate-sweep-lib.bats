#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # BOTH are load-bearing: the state root reads XDG_STATE_HOME first and it IS
  # exported in this environment, so overriding HOME alone would append to the
  # operator's real ledger and contend for the real lock.
  export HOME="$TEST_DIR" XDG_STATE_HOME="$TEST_DIR/state"
  LIB="${LIB_PATH:-$(cd "${BATS_TEST_DIRNAME}/.." && pwd)/pg-go-mutate-sweep.bash}"
  [ -d "$LIB" ] && LIB="$LIB/pg-go-mutate-sweep.bash"
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "${LIB%%:*}"
}

teardown() {
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
}

@test "state root honours XDG_STATE_HOME" {
  run pgms_state_root
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/state/pg-go-mutate-sweep" ]
}

@test "state root falls back to HOME/.local/state" {
  unset XDG_STATE_HOME
  run pgms_state_root
  [ "$output" = "$TEST_DIR/.local/state/pg-go-mutate-sweep" ]
}

@test "slug replaces every path separator" {
  run pgms_slug "internal/gate/deep"
  [ "$output" = "internal__gate__deep" ]
}

@test "unit key round-trips when both halves contain slashes" {
  key="$(pgms_unit_key "repo/packages/pb" "internal/gate")"
  [ "$key" = "repo/packages/pb#internal/gate" ]
  [ "$(pgms_unit_project "$key")" = "repo/packages/pb" ]
  [ "$(pgms_unit_pkg "$key")" = "internal/gate" ]
}

@test "unit key parses on the FIRST hash" {
  [ "$(pgms_unit_pkg "a/b#c/d#e")" = "c/d#e" ]
}
