#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

setup() {
  TEST_DIR="$(mktemp -d)"
  mkdir -p "$TEST_DIR/bin"
  # A stand-in pg-hooks FIRST on PATH: the assembled wrapper only suffixes the
  # real one, so this wins. It records argv and exits with $FAKE_RC.
  cat >"$TEST_DIR/bin/pg-hooks" <<FAKE
#!/bin/sh
printf '%s\n' "\$*" >"$TEST_DIR/argv"
exit "\${FAKE_RC:-0}"
FAKE
  chmod +x "$TEST_DIR/bin/pg-hooks"
  export PATH="$TEST_DIR/bin:$PATH"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SUT="$SCRIPT_UNDER_TEST"
  else
    # Raw source: the script has no library, so it runs as is.
    SUT="$TEST_DIR/pre-commit-fix"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "${BATS_TEST_DIRNAME}/../pre-commit-fix.sh"
    } >"$SUT"
    chmod +x "$SUT"
  fi
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "pre-commit-fix forwards to pg-hooks fix" {
  run "$SUT"
  [ "$status" -eq 0 ]
  [ "$(cat "$TEST_DIR/argv")" = "fix" ]
}

@test "pre-commit-fix passes its arguments through" {
  run "$SUT" --help
  [ "$status" -eq 0 ]
  [ "$(cat "$TEST_DIR/argv")" = "fix --help" ]
}

@test "pre-commit-fix propagates pg-hooks fix's exit code" {
  FAKE_RC=11 run "$SUT"
  [ "$status" -eq 11 ]
  FAKE_RC=10 run "$SUT"
  [ "$status" -eq 10 ]
}
