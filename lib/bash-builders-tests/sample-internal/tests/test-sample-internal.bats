#!/usr/bin/env bats

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
}

@test "sample-internal runs" {
  run bash -euo pipefail "$SCRIPTS_DIR/sample-internal.sh"
  [ "$status" -eq 0 ]
  [ "$output" = "internal command ran" ]
}
