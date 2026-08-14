#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  if [ -n "${SCRIPT_UNDER_TEST:-}" ]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    # Running the raw .sh directly would fail: unlike the assembled artifact
    # the nix check exercises, it has no `source` line of its own -- the
    # builder prepends the library at build time (mkBashScript's
    # `libraries` composition) -- so none of the pgm_* functions the script
    # calls would be in scope. Assemble an equivalent wrapper here,
    # replicating that composition order (library sourced before the
    # command's .sh), per the bash-scripting skill's "Library wrapper
    # pattern" (the same technique modules/pnwf/pnwf/tests/test-pnwf.bats
    # uses for its own SCRIPT_UNDER_TEST fallback).
    local lib_path resolved_lib
    lib_path="${LIB_PATH:-$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pg-go-mutate-lib.bash}"
    if [ -d "$lib_path" ]; then
      resolved_lib="$lib_path/pg-go-mutate-lib.bash"
    else
      resolved_lib="${lib_path%%:*}"
    fi
    cat >"$TEST_DIR/pg-go-mutate-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$resolved_lib"
source "${BATS_TEST_DIRNAME}/../pg-go-mutate.sh"
WRAPPER
    chmod +x "$TEST_DIR/pg-go-mutate-wrapper"
    SCRIPT="$TEST_DIR/pg-go-mutate-wrapper"
  fi
  export SCRIPT
}

teardown() {
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
}

@test "--help exits 0 and documents every flag" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *--tags* ]]
  [[ "$output" == *--json* ]]
  [[ "$output" == *--timeout* ]]
  [[ "$output" == *--workers* ]]
}

@test "--help states that ./... is not accepted" {
  run "$SCRIPT" --help
  [[ "$output" == *"./..."* ]]
}

@test "rejects the ./... package pattern with a clear message" {
  run "$SCRIPT" './...'
  [ "$status" -ne 0 ]
  [[ "$output" == *"./..."* ]]
}

@test "rejects --workers 0 before invoking the engine" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --workers 0 "$TEST_DIR"
  [ "$status" -ne 0 ]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "rejects an unknown flag" {
  run "$SCRIPT" --nope
  [ "$status" -ne 0 ]
}

@test "aborts non-zero on a target that is not a directory or .go file" {
  run "$SCRIPT" "$TEST_DIR/missing"
  [ "$status" -ne 0 ]
}
