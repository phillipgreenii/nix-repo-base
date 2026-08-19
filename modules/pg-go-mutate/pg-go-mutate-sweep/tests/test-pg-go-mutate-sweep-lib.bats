#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # BOTH are load-bearing: the state root reads XDG_STATE_HOME first and it IS
  # exported in this environment, so overriding HOME alone would append to the
  # operator's real ledger and contend for the real lock.
  export HOME="$TEST_DIR" XDG_STATE_HOME="$TEST_DIR/state"
  # SCRIPTS_DIR, never LIB_PATH. In a mkBashScript check LIB_PATH holds the
  # composed DEPENDENCY libraries (here pg-go-mutate-lib), colon-separated -- not
  # this command's own .bash. Reading it as the latter sourced pg-go-mutate-lib
  # instead, so every pgms_* was command-not-found while the local run, where
  # LIB_PATH is unset and the fallback applied, stayed green. SCRIPTS_DIR is the
  # command's own source dir, which is what holds pg-go-mutate-sweep.bash.
  #
  # pg-go-mutate-lib is deliberately NOT sourced here: the only function it would
  # supply, pgm_detect_tags, is stubbed by the two pgms_apply_tags cases below, so
  # sourcing it would mask the stub rather than support it.
  LIB="${SCRIPTS_DIR:-$(cd "${BATS_TEST_DIRNAME}/.." && pwd)}/pg-go-mutate-sweep.bash"
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$LIB"
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

@test "unit project parses on the FIRST hash" {
  [ "$(pgms_unit_project "a/b#c/d#e")" = "a/b" ]
}

_mkmod() { # <dir> <module-path>
  mkdir -p "$1"
  printf 'module %s\n\ngo 1.25.0\n' "$2" >"$1/go.mod"
}

@test "projects exclude vendor, node_modules, worktrees, workforests, fixtures and testdata" {
  _mkmod "$TEST_DIR/ws/repo/a" example.com/a
  _mkmod "$TEST_DIR/ws/repo/vendor/v" example.com/v
  _mkmod "$TEST_DIR/ws/repo/node_modules/n" example.com/n
  _mkmod "$TEST_DIR/ws/repo/.worktrees/w" example.com/w
  _mkmod "$TEST_DIR/ws/repo/.workforests/f" example.com/f
  _mkmod "$TEST_DIR/ws/repo/lib/tests/fixtures/x" example.com/x
  _mkmod "$TEST_DIR/ws/repo/pkg/testdata/t" example.com/t
  run pgms_find_projects "$TEST_DIR/ws"
  [ "$output" = "repo/a" ]
}

@test "units exclude dirs nested under another candidate" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/outer/inner" "$TEST_DIR/ws/p/leaf"
  printf 'package outer\n' >"$TEST_DIR/ws/p/outer/o.go"
  printf 'package inner\n' >"$TEST_DIR/ws/p/outer/inner/i.go"
  printf 'package leaf\n' >"$TEST_DIR/ws/p/leaf/l.go"
  run pgms_find_units "$TEST_DIR/ws" p
  [ "$(printf '%s\n' "$output" | sort | tr '\n' ' ')" = "leaf outer " ]
}

@test "a dir holding only _test.go files is not a candidate" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/only"
  printf 'package only\n' >"$TEST_DIR/ws/p/only/o_test.go"
  run pgms_find_units "$TEST_DIR/ws" p
  [ -z "$output" ]
}

@test "plan orders cheap projects first and subtree units last" {
  _mkmod "$TEST_DIR/ws/big" example.com/big
  mkdir -p "$TEST_DIR/ws/big/aaa" "$TEST_DIR/ws/big/sub/deep"
  printf 'package big\n' >"$TEST_DIR/ws/big/aaa/a.go"
  printf 'package sub\n' >"$TEST_DIR/ws/big/sub/s.go"
  printf 'package deep\n' >"$TEST_DIR/ws/big/sub/deep/d.go"
  _mkmod "$TEST_DIR/ws/small" example.com/small
  mkdir -p "$TEST_DIR/ws/small/one"
  printf 'package one\n' >"$TEST_DIR/ws/small/one/o.go"
  run pgms_plan "$TEST_DIR/ws"
  # small (1 candidate) precedes big (2 kept); within big, leaf aaa precedes subtree sub
  [ "$(printf '%s\n' "$output" | head -1)" = "small#one" ]
  [ "$(printf '%s\n' "$output" | sed -n 2p)" = "big#aaa" ]
  [ "$(printf '%s\n' "$output" | sed -n 3p)" = "big#sub" ]
}

@test "plan is deterministic across invocations" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/b" "$TEST_DIR/ws/p/a"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  first="$(pgms_plan "$TEST_DIR/ws")"
  [ "$first" = "$(pgms_plan "$TEST_DIR/ws")" ]
}

@test "slug collision aborts naming both paths" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/a/b__c" "$TEST_DIR/ws/p/a__b/c"
  printf 'package x\n' >"$TEST_DIR/ws/p/a/b__c/x.go"
  printf 'package y\n' >"$TEST_DIR/ws/p/a__b/c/y.go"
  run pgms_check_slug_collisions "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
  [[ "$output" == *"a/b__c"* ]]
  [[ "$output" == *"a__b/c"* ]]
}

@test "root package plus a subdir yields only the root unit" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/sub"
  printf 'package p\n' >"$TEST_DIR/ws/p/root.go"
  printf 'package sub\n' >"$TEST_DIR/ws/p/sub/s.go"
  run pgms_find_units "$TEST_DIR/ws" p
  [ "$output" = "." ]
}

@test "project key is '.' when go.mod sits at the workspace root" {
  _mkmod "$TEST_DIR/ws" example.com/root
  run pgms_find_projects "$TEST_DIR/ws"
  [ "$output" = "." ]
}

@test "plan tie-breaks equal-count projects by project key" {
  _mkmod "$TEST_DIR/ws/zproj" example.com/z
  mkdir -p "$TEST_DIR/ws/zproj/one"
  printf 'package one\n' >"$TEST_DIR/ws/zproj/one/o.go"
  _mkmod "$TEST_DIR/ws/aproj" example.com/a
  mkdir -p "$TEST_DIR/ws/aproj/one"
  printf 'package one\n' >"$TEST_DIR/ws/aproj/one/o.go"
  run pgms_plan "$TEST_DIR/ws"
  [ "$(printf '%s\n' "$output" | head -1)" = "aproj#one" ]
  [ "$(printf '%s\n' "$output" | sed -n 2p)" = "zproj#one" ]
}

_ledger() { mkdir -p "$(pgms_state_root)"; cat >"$(pgms_ledger_path)"; }

@test "replay keeps the LAST record per unit" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
{"kind":"unit","unit":"p#a","status":"done"}
EOF
  run pgms_unit_status "p#a"
  [ "$output" = "done" ]
}

@test "replay tolerates a truncated final line" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done"}
{"kind":"unit","unit":"p#b","sta
EOF
  run pgms_unit_status "p#a"
  [ "$output" = "done" ]
  run pgms_unit_status "p#b"
  [ -z "$output" ]
}

@test "replay ignores bead records when building unit state" {
  _ledger <<'EOF'
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed"}
{"kind":"unit","unit":"p#a","status":"done"}
EOF
  run pgms_replay_units
  [ "$(printf '%s\n' "$output" | grep -c .)" -eq 1 ]
  [[ "$output" == "p#a	done" ]]
}

@test "a recorded unit does not need a run by default" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
EOF
  run pgms_unit_needs_run "p#a" ""
  [ "$status" -eq 1 ]
}

@test "an unrecorded unit needs a run" {
  mkdir -p "$(pgms_state_root)"; : >"$(pgms_ledger_path)"
  run pgms_unit_needs_run "p#zzz" ""
  [ "$status" -eq 0 ]
}

@test "--retry selects by status and 'transient' expands to the cohort" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
{"kind":"unit","unit":"p#b","status":"done"}
{"kind":"unit","unit":"p#c","status":"timeout"}
EOF
  run pgms_unit_needs_run "p#a" "failed";    [ "$status" -eq 0 ]
  run pgms_unit_needs_run "p#b" "failed";    [ "$status" -eq 1 ]
  run pgms_unit_needs_run "p#c" "transient"; [ "$status" -eq 0 ]
  run pgms_unit_needs_run "p#b" "transient"; [ "$status" -eq 1 ]
}

@test "append writes one line and creates the state root" {
  pgms_append_record '{"kind":"unit","unit":"p#a","status":"done"}'
  [ "$(grep -c . "$(pgms_ledger_path)")" -eq 1 ]
}

_proj_with_two_units() {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/p/b"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
}

@test "bead is not due while a unit is unrecorded" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "bead is due once every unit is recorded" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "file" ]
}

@test "bead is due on a fresh invocation that runs ZERO units" {
  # The lost-project regression: the process died after the last unit record but
  # before bd create, so a resumed sweep runs nothing for this project.
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
}

@test "bead is not due once a bead record exists" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed","finished":"2026-08-17T10:02:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "bead is due again when a unit record is NEWER than the bead record" {
  # Without this a project whose units all failed keeps a bead of failures and
  # the worklists a later --retry produces can never reach any bead.
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"failed","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed","finished":"2026-08-17T10:02:00-04:00"}
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T11:00:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "amend" ]
}

@test "a suppressed marker has no id, so the action is file rather than amend" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","action":"suppressed","finished":"2026-08-17T10:02:00-04:00"}
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T11:00:00-04:00"}
EOF
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "file" ]
}

@test "a suppressed marker keeps the bead not-due on a plain re-run" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","action":"suppressed","finished":"2026-08-17T10:02:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "a zero-unit project is never due" {
  _mkmod "$TEST_DIR/ws/empty" example.com/empty
  run pgms_bead_due "$TEST_DIR/ws" empty
  [ "$status" -eq 1 ]
}

@test "lock acquires, refuses a live holder, and releases" {
  run pgms_lock_acquire
  [ "$status" -eq 0 ]
  [ -d "$(pgms_state_root)/lock" ]
  run pgms_lock_acquire
  [ "$status" -eq 3 ]
  pgms_lock_release
  [ ! -d "$(pgms_state_root)/lock" ]
}

@test "lock reclaims a stale holder" {
  mkdir -p "$(pgms_state_root)/lock"
  # PID 99999 is not running; the stamp format is "<pid> <iso8601>"
  printf '99999 2026-08-17T10:00:00-04:00\n' >"$(pgms_state_root)/lock/holder"
  run pgms_lock_acquire
  [ "$status" -eq 0 ]
  [ "$(awk '{print $1}' "$(pgms_state_root)/lock/holder")" = "$$" ]
}

@test "a leftover lock.stale directory does not corrupt the reclaim" {
  mkdir -p "$(pgms_state_root)/lock"
  printf '99999 2026-08-17T10:00:00-04:00\n' >"$(pgms_state_root)/lock/holder"

  # Force the collision deterministically, two ways at once, so it lands
  # wherever the ACTUAL implementation under test looks:
  #   (a) "pinned" -- a bash `mktemp` shadowing the binary the shipped
  #       mktemp-d+rmdir dance calls. Faithful to the real contract (fails,
  #       prints nothing, if the target already exists) but pinned to a known
  #       name instead of a random one, so a leftover can be planted at it.
  #   (b) "guessable" -- the literal "$root/lock.stale.$$" name a regression
  #       to a naive `mv lock lock.stale.$$` (the exact defect the design's
  #       atomic-rename comment warns about) would compute directly, with no
  #       mktemp call to intercept. `$$` is stable across `run` (verified: the
  #       PID printed inside a `run`-invoked function matches the PID printed
  #       in the test body outrightly), so this name is predictable up front.
  # Each carries a sentinel that must never be silently destroyed by someone
  # else's cleanup -- the design's own warning: a plain `mv` onto an existing
  # directory nests silently and returns 0, so only the FOLLOW-UP `rm -rf`
  # actually destroys anything, and only if the destination was reused.
  local pinned guessable
  pinned="$(pgms_state_root)/lock.stale.pinned"
  guessable="$(pgms_state_root)/lock.stale.$$"
  # shellcheck disable=SC2317  # invoked indirectly via mktemp -d from the sourced library
  mktemp() {
    [ -e "$pinned" ] && return 1
    mkdir "$pinned"
    printf '%s\n' "$pinned"
  }
  mkdir -p "$pinned" "$guessable"
  printf 'sentinel\n' >"$pinned/sentinel"
  printf 'sentinel\n' >"$guessable/sentinel"

  run pgms_lock_acquire
  if [ "$status" -eq 0 ]; then
    # Neither racer's directory was silently absorbed.
    [ -f "$pinned/sentinel" ]
    [ -f "$guessable/sentinel" ]
  else
    [ "$status" -eq 3 ]
  fi
}

@test "classification maps every exit code" {
  [ "$(pgms_classify 10)"  = "no-tests" ]
  [ "$(pgms_classify 11)"  = "not-enumerable" ]
  [ "$(pgms_classify 12)"  = "unhealthy" ]
  [ "$(pgms_classify 14)"  = "vanished" ]
  [ "$(pgms_classify 124)" = "timeout" ]
  [ "$(pgms_classify 13)"  = "fatal" ]
  [ "$(pgms_classify 2)"   = "fatal" ]
  [ "$(pgms_classify 1)"   = "failed" ]
  [ "$(pgms_classify 99)"  = "failed" ]
}

@test "137 is failed, NOT timeout" {
  # timeout(1) returns 124 whether or not it escalated to KILL; 137 means
  # timeout ITSELF was killed, e.g. OOM.
  [ "$(pgms_classify 137)" = "failed" ]
}

@test "exit 0 with a low timed-out fraction is done" {
  cat >"$TEST_DIR/r.json" <<'EOF'
{"statistics":{"killed":90,"survived":8,"notViable":1,"timedOut":1,"errors":0}}
EOF
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "done" ]
}

@test "exit 0 with a high timed-out fraction is inconclusive" {
  cat >"$TEST_DIR/r.json" <<'EOF'
{"statistics":{"killed":0,"survived":0,"notViable":0,"timedOut":100,"errors":0}}
EOF
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "inconclusive" ]
}

@test "a zero denominator is failed, not a division error" {
  # A report can carry totalMutants > 0 with an empty statistics object and still
  # pass pgm_report_sane, so the fraction must be guarded.
  printf '{"statistics":{}}\n' >"$TEST_DIR/r.json"
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "failed" ]
}

@test "tags are gated by the allowlist and default to none applied" {
  pgm_detect_tags() { printf 'contract,hostile\n'; }
  run pgms_apply_tags "$TEST_DIR" ""
  [ "$output" = "	contract,hostile" ]
  run pgms_apply_tags "$TEST_DIR" "contract"
  [ "$output" = "contract	hostile" ]
}

@test "no detected tags yields both fields empty" {
  pgm_detect_tags() { printf '\n'; }
  run pgms_apply_tags "$TEST_DIR" "contract"
  [ "$output" = "	" ]
}
