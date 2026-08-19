#!/usr/bin/env bats

# Required by the `run !` form below (bats >= 1.5.0); without it bats emits BW02.
bats_require_minimum_version 1.5.0

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

@test "a failing unit records failed and the sweep CONTINUES" {
  # The errexit regression: the builder injects set -euo pipefail, so the loop
  # must capture the exit status with `rc=0; cmd || rc=$?`.
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/p/b"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; exit 1'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 2 ]
  grep -q '"status":"failed"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "exit 13 aborts the whole sweep with exit 4" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 13'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
}

@test "exit 2 from the engine aborts as a sweep bug" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 2'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
}

@test "exit 14 records vanished and continues" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 14'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  grep -q '"status":"vanished"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "a high timed-out fraction records inconclusive, not done" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":0,\"survived\":0,\"notViable\":0,\"timedOut\":10,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q '"status":"inconclusive"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "the engine is invoked with --json and the report is stored" {
  _ws_one_unit
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q -- '--json' "$TEST_DIR/calls"
  [ -f "$TEST_DIR/state/pg-go-mutate-sweep/runs/p/a.json" ]
}

@test "--tags is passed ONLY when a tag is applied" {
  _ws_one_unit
  printf '//go:build contract\n\npackage a\n' >"$TEST_DIR/ws/p/a/a_test.go"
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  # `run !`, not a bare `!`: bash exempts a !-inverted pipeline from errexit, so a
  # bare `! grep` mid-test asserts NOTHING (shellcheck SC2314).
  run ! grep -q -- '--tags' "$TEST_DIR/calls"
  : >"$TEST_DIR/calls"
  run "$SCRIPT" --root "$TEST_DIR/ws" --redo 'p#a' --auto-tags contract
  grep -q -- '--tags contract' "$TEST_DIR/calls"
}

@test "the watchdog kills the whole subtree" {
  # This is the test that would have caught `timeout --foreground`, in which
  # children of COMMAND are NOT timed out.
  _ws_one_unit
  _stub pg-go-mutate 'bash -c "sleep 300 & echo \$! >\"$TEST_DIR/grandchild\"; sleep 300"'
  _stub bd 'echo pg2-stub'
  # --unit-timeout 2, not 1: the window has to be wide enough for the stub to fork
  # the background sleep and record its pid, or there is no grandchild to kill and
  # the run asserts nothing (the setup check below turns that into its own red).
  # --unit-kill-grace stays 1 -- a `sleep` dies on the TERM, so the TERM->KILL
  # escalation is not the mechanism this test depends on, and widening it would not
  # have prevented the observed failure.
  run "$SCRIPT" --root "$TEST_DIR/ws" --unit-timeout 2 --unit-kill-grace 1
  [ "$status" -eq 0 ]
  grep -q '"status":"timeout"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"

  gc="$(cat "$TEST_DIR/grandchild" 2>/dev/null || true)"
  # Its own assertion, NOT the old `[ -n "$gc" ] && ! kill -0 "$gc"`: an empty $gc is
  # a SETUP failure -- the stub never established the subtree -- and the combined form
  # reported it with the same red as a grandchild that SURVIVED, which is the opposite
  # defect. A reader has to be able to tell those apart from the failure output.
  if [ -z "$gc" ]; then
    printf 'no grandchild pid recorded at %s: the stub never established the subtree, so this run asserts NOTHING about the watchdog\n' \
      "$TEST_DIR/grandchild" >&2
    return 1
  fi

  # POLL to a bounded deadline instead of probing once. `timeout` returns as soon as
  # its DIRECT child is gone, while signal delivery to the rest of the process group
  # and the reaping of the grandchild are asynchronous -- so a single `kill -0` right
  # after `run` loses a race for which this package's own batsJobs=4 supplies the load
  # (pg2-46lx6: the byte-identical check derivation failed, then passed on an immediate
  # retry). The property asserted is unchanged, and is the one that matters -- the
  # grandchild DOES die -- it is simply no longer pinned to a single instant.
  #
  # Residual risk, deliberately not chased: `kill -0` answers "some process holds this
  # pid", not "the grandchild lives", so a recycled pid could keep the loop spinning.
  # The loop exits on the first negative probe, so the observation window is
  # milliseconds in the passing case and pids are handed out monotonically; every
  # identity check available (ps, lsof) would be a new dependency the nix check
  # environment does not carry, so this is accepted rather than hardened.
  gc_started=$SECONDS
  gc_deadline=$((gc_started + 20))
  while kill -0 "$gc" 2>/dev/null; do
    if [ "$SECONDS" -ge "$gc_deadline" ]; then
      printf 'grandchild %s STILL ALIVE %ss after the sweep exited (polled every 0.1s to a %ss deadline): the watchdog did not kill the whole subtree\n' \
        "$gc" "$((SECONDS - gc_started))" "$((gc_deadline - gc_started))" >&2
      # Diagnostics only -- `ps` is not guaranteed in the nix check environment, so it
      # must never be the assertion itself.
      if command -v ps >/dev/null 2>&1; then
        ps -p "$gc" -o pid=,ppid=,stat=,command= >&2 || true
      fi
      return 1
    fi
    sleep 0.1
  done
}

@test "a resumed run redoes nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 1 ]
}

@test "--retry re-attempts only the selected statuses" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; exit 1'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  run "$SCRIPT" --root "$TEST_DIR/ws" --retry failed
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 2 ]
}

@test "--only restricts the run list but not the plan" {
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/q/b"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'module example.com/q\n\ngo 1.25.0\n' >"$TEST_DIR/ws/q/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/q/b/b.go"
  _stub pg-go-mutate 'echo "$PWD" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --only p
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 1 ]
}

@test "the bead is filed exactly once per project" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$(grep -c 'create' "$TEST_DIR/bdcalls")" -eq 1 ]
  : >"$TEST_DIR/bdcalls"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ ! -s "$TEST_DIR/bdcalls" ]
}

@test "a resumed run with ZERO units left still files the pending bead" {
  # The lost-project regression at script level: the ledger has every unit but
  # no bead record, exactly as a crash between the two would leave it.
  _ws_one_unit
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep"
  cat >"$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl" <<'EOF'
{"kind":"unit","unit":"p#a","project":"p","pkg":"a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
EOF
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ ! -f "$TEST_DIR/calls" ]
  [ "$(grep -c 'create' "$TEST_DIR/bdcalls")" -eq 1 ]
}

@test "a --retry that produces a newer record AMENDS by comment" {
  _ws_one_unit
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep"
  cat >"$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl" <<'EOF'
{"kind":"unit","unit":"p#a","project":"p","pkg":"a","status":"failed","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-existing","action":"filed","finished":"2026-08-17T10:01:00-04:00"}
EOF
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --retry failed
  grep -q 'comment pg2-existing' "$TEST_DIR/bdcalls"
  ! grep -q 'create' "$TEST_DIR/bdcalls"
}

@test "a failing bd does not abort the sweep and leaves no bead record" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'exit 7'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  ! grep -q '"kind":"bead"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "--no-beads files nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --no-beads
  [ ! -f "$TEST_DIR/bdcalls" ]
}

@test "the bead body carries the protocol and no mutant count" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":7,\"survived\":3,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'while [ $# -gt 0 ]; do [ "$1" = "--body-file" ] && cp "$2" "$TEST_DIR/body.md"; shift; done; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q 'tags_withheld' "$TEST_DIR/body.md"
  grep -q 'Record NO scores' "$TEST_DIR/body.md"
  # N1: the body must not leak the engine's survivor count
  ! grep -qE '(^|[^0-9])3 surviv' "$TEST_DIR/body.md"
}
