#!/usr/bin/env bats

# Test suite for sample-cmd

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    # Local dev: resolve from test file location
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
}

run_cmd() {
  run bash -euo pipefail "$SCRIPTS_DIR/sample-cmd.sh" "$@"
}

@test "default greeting" {
  run_cmd
  [ "$status" -eq 0 ]
  [ "$output" = "hello, world" ]
}

@test "named greeting" {
  run_cmd Alice
  [ "$status" -eq 0 ]
  [ "$output" = "hello, Alice" ]
}

@test "--upper flag converts to uppercase" {
  run_cmd --upper
  [ "$status" -eq 0 ]
  [ "$output" = "HELLO, WORLD" ]
}

@test "--upper with name" {
  run_cmd --upper Alice
  [ "$status" -eq 0 ]
  [ "$output" = "HELLO, ALICE" ]
}

@test "--help shows usage text" {
  run_cmd --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: sample-cmd"* ]]
}

@test "unknown flag rejected" {
  run_cmd --bogus
  [ "$status" -eq 1 ]
  [[ "$output" == *"Unknown option: --bogus"* ]]
}
