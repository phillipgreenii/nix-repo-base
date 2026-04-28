#!/usr/bin/env bats

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: resolve from test file location
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../sample-lib" && pwd)/sample-lib.bash"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "uses library function to greet" {
  run bash -euo pipefail -c "source '${LIB_PATH}'; source '${SCRIPTS_DIR}/sample-cmd-with-lib.sh'" _ Alice
  [ "$status" -eq 0 ]
  [[ "$output" == *"Alice"* ]]
}

@test "fails without name argument" {
  run bash -euo pipefail -c "source '${LIB_PATH}'; source '${SCRIPTS_DIR}/sample-cmd-with-lib.sh'"
  [ "$status" -ne 0 ]
  [[ "$output" == *"NAME required"* ]]
}
