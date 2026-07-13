#!/usr/bin/env bats

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Set up config vars (simulates what the builder injects). SAMPLE_GREETING is
  # set per-test inside the `bash -c` blocks below, not here.
  echo '{"name": "testuser"}' > "$TEST_DIR/config.json"
  SAMPLE_CONFIG="$TEST_DIR/config.json"
  export SAMPLE_EXPORTED="exported-value"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "reads scalar config" {
  run bash -euo pipefail -c "
    SAMPLE_GREETING='howdy'
    SAMPLE_CONFIG='${SAMPLE_CONFIG}'
    export SAMPLE_EXPORTED='exported-value'
    source '${SCRIPTS_DIR}/sample-cmd-with-config.sh'
  "
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "greeting: howdy"
}

@test "reads JSON config" {
  run bash -euo pipefail -c "
    SAMPLE_GREETING='howdy'
    SAMPLE_CONFIG='${SAMPLE_CONFIG}'
    export SAMPLE_EXPORTED='exported-value'
    source '${SCRIPTS_DIR}/sample-cmd-with-config.sh'
  "
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "name: testuser"
}

@test "reads exported config" {
  run bash -euo pipefail -c "
    SAMPLE_GREETING='howdy'
    SAMPLE_CONFIG='${SAMPLE_CONFIG}'
    export SAMPLE_EXPORTED='exported-value'
    source '${SCRIPTS_DIR}/sample-cmd-with-config.sh'
  "
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "exported: exported-value"
}

# Drives the ASSEMBLED artifact so the REAL injected SAMPLE_PORT is exercised
# (bead pg2-jucnb): a non-string scalar must be inlined (SAMPLE_PORT=8080), not
# assigned a /nix/store/...json file path.
@test "injects a non-string scalar inline, not as a JSON file path" {
  [ -n "${SCRIPT_UNDER_TEST:-}" ] || skip "SCRIPT_UNDER_TEST not set (raw-source run)"
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "port: 8080"
  ! echo "$output" | grep -q "port: /nix/store"
}

# Drives the ASSEMBLED artifact so the REAL injected bool config is exercised
# (bead pg2-jucnb): a bool must inline as the unambiguous literal true/false via
# lib.boolToString, NOT toString's "1"/"" — which would emit `flag: 1` and
# `flag_off:` (empty, reads as unset).
@test "injects a bool scalar as a literal true/false, not toString's 1/empty" {
  [ -n "${SCRIPT_UNDER_TEST:-}" ] || skip "SCRIPT_UNDER_TEST not set (raw-source run)"
  run "$SCRIPT_UNDER_TEST"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qx "flag: true"
  echo "$output" | grep -qx "flag_off: false"
}
