#!/usr/bin/env bats
# bats file_tags=type:unit
#
# CLI suite for gogate, driving the ASSEMBLED artifact via SCRIPT_UNDER_TEST
# (bead pg2-28wwb convention): in the nix check that is the real wrapped
# binary; for a local `bats tests/` run setup() below assembles an
# equivalent wrapper sourcing gogate.sh under strict mode -- gogate has no
# separate .bash library (unlike pg-go-mutate/pg-test-runner), so there is
# nothing to source ahead of it.
#
# `go` and `gofmt` are entirely MOCKED (stub scripts prepended to PATH) so
# every test here is deterministic and needs no real Go module: each mock
# logs its full argv to GOGATE_TEST_CALLS_LOG and then exits/prints per the
# GOGATE_TEST_*_STATUS / GOGATE_TEST_*_OUTPUT env vars the test sets before
# invoking $SCRIPT. This also lets each test assert a later stage was never
# reached, by checking the calls log has no entry for it.

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
}

setup() {
  if [[ -n "${SCRIPT_UNDER_TEST:-}" ]]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    TEST_WRAPPER_DIR="$(mktemp -d)"
    cat >"$TEST_WRAPPER_DIR/gogate-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$SCRIPTS_DIR/gogate.sh"
WRAPPER
    chmod +x "$TEST_WRAPPER_DIR/gogate-wrapper"
    SCRIPT="$TEST_WRAPPER_DIR/gogate-wrapper"
  fi
  export SCRIPT

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  MOCK_DIR="$TEST_DIR/mocks"
  mkdir -p "$MOCK_DIR"

  GOGATE_TEST_CALLS_LOG="$TEST_DIR/calls.log"
  : >"$GOGATE_TEST_CALLS_LOG"
  export GOGATE_TEST_CALLS_LOG

  cat >"$MOCK_DIR/gofmt" <<'MOCK'
#!/usr/bin/env bash
echo "gofmt $*" >>"$GOGATE_TEST_CALLS_LOG"
printf '%s' "${GOGATE_TEST_FMT_OUTPUT:-}"
exit "${GOGATE_TEST_FMT_STATUS:-0}"
MOCK
  chmod +x "$MOCK_DIR/gofmt"

  cat >"$MOCK_DIR/go" <<'MOCK'
#!/usr/bin/env bash
echo "go $*" >>"$GOGATE_TEST_CALLS_LOG"
case "$1" in
build)
  printf '%s' "${GOGATE_TEST_BUILD_OUTPUT:-}"
  exit "${GOGATE_TEST_BUILD_STATUS:-0}"
  ;;
vet)
  printf '%s' "${GOGATE_TEST_VET_OUTPUT:-}"
  exit "${GOGATE_TEST_VET_STATUS:-0}"
  ;;
test)
  printf '%s' "${GOGATE_TEST_TEST_OUTPUT:-}"
  exit "${GOGATE_TEST_TEST_STATUS:-0}"
  ;;
*)
  exit 0
  ;;
esac
MOCK
  chmod +x "$MOCK_DIR/go"

  export PATH="$MOCK_DIR:$PATH"

  # Defaults: every stage mocks a clean pass unless a test overrides one.
  export GOGATE_TEST_FMT_STATUS=0 GOGATE_TEST_FMT_OUTPUT=""
  export GOGATE_TEST_BUILD_STATUS=0 GOGATE_TEST_BUILD_OUTPUT=""
  export GOGATE_TEST_VET_STATUS=0 GOGATE_TEST_VET_OUTPUT=""
  export GOGATE_TEST_TEST_STATUS=0 GOGATE_TEST_TEST_OUTPUT=""
}

teardown() {
  [[ -n "${TEST_DIR:-}" ]] && rm -rf "$TEST_DIR"
  [[ -n "${TEST_WRAPPER_DIR:-}" ]] && rm -rf "$TEST_WRAPPER_DIR"
}

# --- --help -----------------------------------------------------------------

@test "--help exits 0 and documents every flag and stage" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"--pkg"* ]]
  [[ "$output" == *"--quick"* ]]
  [[ "$output" == *"gofmt"* ]]
  [[ "$output" == *"go build"* ]]
  [[ "$output" == *"go vet"* ]]
  [[ "$output" == *"go test"* ]]
  [[ "$output" == *"GOGATE: PASS"* ]]
  [[ "$output" == *"GOGATE: FAIL"* ]]
}

@test "--help documents the -- passthrough syntax" {
  run "$SCRIPT" --help
  [[ "$output" == *"-run TestFoo -count=1"* ]]
}

# --- usage errors ------------------------------------------------------------

@test "unknown flag is a usage error (exit 2)" {
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
}

@test "--pkg with no value is a usage error (exit 2)" {
  run "$SCRIPT" --pkg
  [ "$status" -eq 2 ]
}

@test "an unexpected positional argument is a usage error (exit 2)" {
  run "$SCRIPT" ./internal/foo
  [ "$status" -eq 2 ]
}

# --- happy path ---------------------------------------------------------------

@test "all stages passing prints exactly GOGATE: PASS and exits 0" {
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "GOGATE: PASS" ]
}

@test "runs fmt, build, vet, test in order on a full pass" {
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" == *"gofmt -l ."* ]]
  [[ "$log" == *"go build ./..."* ]]
  [[ "$log" == *"go vet ./..."* ]]
  [[ "$log" == *"go test ./..."* ]]
}

# --- stage ordering / stop-at-first-failure ----------------------------------

@test "gofmt failure stops before build/vet/test run" {
  GOGATE_TEST_FMT_OUTPUT="internal/foo/bar.go"
  run "$SCRIPT"
  [ "$status" -eq 10 ]
  [[ "$output" == *"internal/foo/bar.go"* ]]
  [[ "$output" == *"GOGATE: FAIL(fmt)"* ]]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" != *"go build"* ]]
  [[ "$log" != *"go vet"* ]]
  [[ "$log" != *"go test"* ]]
}

@test "a non-zero gofmt exit (parse failure) also fails the fmt stage" {
  GOGATE_TEST_FMT_STATUS=2
  GOGATE_TEST_FMT_OUTPUT="gofmt: bad.go:3:1: expected declaration, found EOF"
  run "$SCRIPT"
  [ "$status" -eq 10 ]
  [[ "$output" == *"GOGATE: FAIL(fmt)"* ]]
}

@test "build failure stops before vet/test run" {
  GOGATE_TEST_BUILD_STATUS=1
  GOGATE_TEST_BUILD_OUTPUT="internal/foo/bar.go:10:2: undefined: Baz"
  run "$SCRIPT"
  [ "$status" -eq 11 ]
  [[ "$output" == *"undefined: Baz"* ]]
  [[ "$output" == *"GOGATE: FAIL(build)"* ]]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" != *"go vet"* ]]
  [[ "$log" != *"go test"* ]]
}

@test "vet failure stops before test runs" {
  GOGATE_TEST_VET_STATUS=1
  GOGATE_TEST_VET_OUTPUT="internal/foo/bar.go:5:2: result of fmt.Sprintf call not used"
  run "$SCRIPT"
  [ "$status" -eq 12 ]
  [[ "$output" == *"result of fmt.Sprintf call not used"* ]]
  [[ "$output" == *"GOGATE: FAIL(vet)"* ]]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" != *"go test"* ]]
}

@test "test failure is the final stage and reports FAIL(test)" {
  GOGATE_TEST_TEST_STATUS=1
  GOGATE_TEST_TEST_OUTPUT="--- FAIL: TestFoo (0.00s)"
  run "$SCRIPT"
  [ "$status" -eq 13 ]
  [[ "$output" == *"FAIL: TestFoo"* ]]
  [[ "$output" == *"GOGATE: FAIL(test)"* ]]
}

# --- --quick ------------------------------------------------------------------

@test "--quick skips the test stage entirely on a pass" {
  run "$SCRIPT" --quick
  [ "$status" -eq 0 ]
  [ "$output" = "GOGATE: PASS" ]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" != *"go test"* ]]
  [[ "$log" == *"go build"* ]]
  [[ "$log" == *"go vet"* ]]
}

@test "--quick still fails on a vet failure" {
  GOGATE_TEST_VET_STATUS=1
  GOGATE_TEST_VET_OUTPUT="vet problem"
  run "$SCRIPT" --quick
  [ "$status" -eq 12 ]
  [[ "$output" == *"GOGATE: FAIL(vet)"* ]]
}

# --- --pkg scoping -------------------------------------------------------------

@test "--pkg is forwarded to build/vet/test but not to gofmt" {
  run "$SCRIPT" --pkg ./internal/collect
  [ "$status" -eq 0 ]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" == *"gofmt -l ."* ]]
  [[ "$log" == *"go build ./internal/collect"* ]]
  [[ "$log" == *"go vet ./internal/collect"* ]]
  [[ "$log" == *"go test ./internal/collect"* ]]
}

# --- -- passthrough -------------------------------------------------------------

@test "flags after -- are appended verbatim to go test" {
  run "$SCRIPT" -- -run TestFoo -count=1
  [ "$status" -eq 0 ]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" == *"go test ./... -run TestFoo -count=1"* ]]
}

@test "--pkg combined with -- passthrough forwards both" {
  run "$SCRIPT" --pkg ./internal/collect -- -run TestFoo
  [ "$status" -eq 0 ]
  local log
  log="$(cat "$GOGATE_TEST_CALLS_LOG")"
  [[ "$log" == *"go test ./internal/collect -run TestFoo"* ]]
}

# --- truncation -----------------------------------------------------------------

@test "failing stage output is truncated to the last 60 lines" {
  local many_lines
  many_lines="$(seq 1 200)"
  GOGATE_TEST_BUILD_STATUS=1
  GOGATE_TEST_BUILD_OUTPUT="$many_lines"
  run "$SCRIPT"
  [ "$status" -eq 11 ]
  # Output = 60 truncated lines + the GOGATE: FAIL(build) verdict line.
  [ "${#lines[@]}" -eq 61 ]
  [ "${lines[0]}" = "141" ]
  [ "${lines[59]}" = "200" ]
  [ "${lines[60]}" = "GOGATE: FAIL(build)" ]
}

@test "output shorter than the tail limit is printed in full, untruncated" {
  GOGATE_TEST_VET_STATUS=1
  GOGATE_TEST_VET_OUTPUT="$(printf 'line-a\nline-b\nline-c')"
  run "$SCRIPT"
  [ "$status" -eq 12 ]
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[0]}" = "line-a" ]
  [ "${lines[2]}" = "line-c" ]
  [ "${lines[3]}" = "GOGATE: FAIL(vet)" ]
}
