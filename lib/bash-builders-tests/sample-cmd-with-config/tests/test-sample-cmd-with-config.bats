#!/usr/bin/env bats

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Set up config vars (simulates what the builder injects)
  SAMPLE_GREETING="howdy"
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
