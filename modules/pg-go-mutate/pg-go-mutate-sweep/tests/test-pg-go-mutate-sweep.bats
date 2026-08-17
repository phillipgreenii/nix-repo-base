#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR" XDG_STATE_HOME="$TEST_DIR/state" GOCACHE="$TEST_DIR/go-build"
  export BIN="$TEST_DIR/bin"
  mkdir -p "$BIN"
  PATH="$BIN:$PATH"; export PATH

  if [ -n "${SCRIPT_UNDER_TEST:-}" ]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    # Assemble what the builder assembles: pg-go-mutate-lib, then this command's
    # own library, then the .sh -- in that composition order.
    local pgm_lib sweep_lib
    pgm_lib="$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pg-go-mutate-lib.bash"
    sweep_lib="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)/pg-go-mutate-sweep.bash"
    cat >"$TEST_DIR/sweep-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$pgm_lib"
source "$sweep_lib"
source "${BATS_TEST_DIRNAME}/../pg-go-mutate-sweep.sh"
WRAPPER
    chmod +x "$TEST_DIR/sweep-wrapper"
    SCRIPT="$TEST_DIR/sweep-wrapper"
  fi
  export SCRIPT
}

teardown() { [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"; }

_stub() { # <name> <body>
  printf '#!/usr/bin/env bash\n%s\n' "$2" >"$BIN/$1"
  chmod +x "$BIN/$1"
}

_ws_one_unit() {
  mkdir -p "$TEST_DIR/ws/p/a"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
}

@test "--help exits 0 and documents every flag" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  for f in --root --only --unit-timeout --unit-kill-grace --mutant-timeout --workers \
           --auto-tags --retry --redo --dry-run --no-beads --force-unlock; do
    [[ "$output" == *"$f"* ]] || { echo "missing $f"; false; }
  done
}

@test "--help documents that a unit may be a directory SUBTREE" {
  run "$SCRIPT" --help
  [[ "$output" == *"subtree"* || "$output" == *"SUBTREE"* ]]
}

@test "rejects an unknown flag with exit 2" {
  run "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}

@test "rejects an invalid --auto-tags value at parse time" {
  run "$SCRIPT" --auto-tags 'x -toolexec=/bin/sh' --dry-run --root "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
}

@test "preflight fails when pg-go-mutate is absent" {
  _ws_one_unit
  _stub bd 'exit 0'
  run env PATH="$BIN:/usr/bin:/bin" "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
  [[ "$output" == *"pg-go-mutate"* ]]
}

@test "preflight fails when bd is absent" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 0'
  run env PATH="$BIN:/usr/bin:/bin" "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
  [[ "$output" == *"bd"* ]]
}

@test "--dry-run prints the plan and runs nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/ran"; exit 0'
  _stub bd 'exit 0'
  run "$SCRIPT" --root "$TEST_DIR/ws" --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"p#a"* ]]
  [ ! -f "$TEST_DIR/ran" ]
}

@test "a slug collision aborts with exit 2 and releases the lock" {
  mkdir -p "$TEST_DIR/ws/p/a/b__c" "$TEST_DIR/ws/p/a__b/c"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package x\n' >"$TEST_DIR/ws/p/a/b__c/x.go"
  printf 'package y\n' >"$TEST_DIR/ws/p/a__b/c/y.go"
  _stub pg-go-mutate 'exit 0'
  _stub bd 'exit 0'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
  [ ! -d "$TEST_DIR/state/pg-go-mutate-sweep/lock" ]
}

@test "refuses to run while another sweep holds the lock" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 0'
  _stub bd 'exit 0'
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep/lock"
  printf '%s %s\n' "$$" "2026-08-17T10:00:00-04:00" \
    >"$TEST_DIR/state/pg-go-mutate-sweep/lock/holder"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 3 ]
}

@test "--force-unlock breaks a live-looking lock" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep/lock"
  printf '%s %s\n' "$$" "2026-08-17T10:00:00-04:00" \
    >"$TEST_DIR/state/pg-go-mutate-sweep/lock/holder"
  run "$SCRIPT" --root "$TEST_DIR/ws" --force-unlock
  [ "$status" -eq 0 ]
}
