#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # HOME is read-only in the nix check sandbox ($HOME=/homeless-shelter), so
  # `go` cannot create its build cache there and every go list/vet/test call
  # fails outright -- masking the guards under test behind an unrelated
  # environment error. Same reason the library suite does this, and the same
  # HOME/GOCACHE-under-TMPDIR convention lib/go-builders.nix uses.
  export HOME="$TEST_DIR" GOCACHE="$TEST_DIR/go-build"

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
  # The interrupt test starts a long-lived stub engine and kills it as part of its
  # assertions; if that test fails midway the sandbox would sit waiting on it.
  if [ -n "${PGM_TEST_PID_LOG:-}" ] && [ -s "${PGM_TEST_PID_LOG}" ]; then
    kill -KILL "$(cat "$PGM_TEST_PID_LOG")" 2>/dev/null || true
  fi
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

@test "rejects --timeout 0 before invoking the engine" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --timeout 0 "$TEST_DIR"
  [ "$status" -ne 0 ]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "rejects an unknown flag" {
  run "$SCRIPT" --nope
  [ "$status" -ne 0 ]
}

@test "aborts non-zero on a target that does not exist" {
  run "$SCRIPT" "$TEST_DIR/missing"
  [ "$status" -ne 0 ]
}

@test "rejects a single .go file target instead of misdiagnosing it as untested" {
  printf 'package fixture\n' >"$TEST_DIR/a.go"
  run "$SCRIPT" "$TEST_DIR/a.go"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not a directory"* ]]
  # The old message sent an agent off to write a test that already exists.
  [[ "$output" != *"Write a test first"* ]]
}

@test "-- does not discard the target that follows it" {
  run "$SCRIPT" -- "$TEST_DIR/missing"
  [ "$status" -ne 0 ]
  # A bare `break` on `--` dropped the target and silently analysed `.` instead.
  [[ "$output" == *"$TEST_DIR/missing"* ]]
}

@test "--tags with no value fails with the standard error shape" {
  run "$SCRIPT" --tags
  [ "$status" -eq 2 ]
  [[ "$output" == *"pg-go-mutate: --tags needs a value"* ]]
}

@test "--tags rejects a value that would inject go flags" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --tags 'contract -toolexec=/bin/sh' "$TEST_DIR"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--tags must be"* ]]
  [ ! -e "$TEST_DIR/ran" ]
}

# --- fixtures for the end-to-end cases below ---------------------------------

# A module with a passing test, so every §5.1 guard is satisfied and the run
# reaches the engine.
make_module() {
  local dir="$TEST_DIR/$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF'
module example.com/fixture

go 1.25
EOF
  printf 'package fixture\n\nfunc Add(a, b int) int { return a + b }\n' >"$dir/fixture.go"
  cat >"$dir/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
  printf '%s\n' "$dir"
}

# A stub engine that writes a canned report with one real survivor into its cwd,
# via the PG_GO_MUTATE_GOMU seam (spec T7 -- never a PATH-prepended stub, which
# would only work because --suffix PATH leaves the pin breakable).
write_survivor_stub() {
  local target="$1" stub="$TEST_DIR/stub-gomu"
  cat >"$stub" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = version ]; then
  printf 'gomu version 0.2.1\n'
  exit 0
fi
cat >mutation-report.json <<JSON
{
  "totalMutants": 3,
  "killedMutants": 0,
  "results": [
    { "mutant": { "id": "$target/fixture.go_0", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "conditional_binary",
        "original": "+", "mutated": "-",
        "description": "Replace + with -" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$target/fixture.go_1", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "return_zero_value",
        "original": "0", "mutated": "0",
        "description": "Replace return 0 with return 0" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$target/fixture.go_2", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "conditional_binary",
        "original": "+", "mutated": "*",
        "description": "Replace + with *" },
      "status": "KILLED", "output": "", "error": "" }
  ],
  "statistics": { "killed": 1, "survived": 2, "timedOut": 0, "errors": 0,
                  "notViable": 0, "mutationScore": 33.3 }
}
JSON
EOF
  chmod +x "$stub"
  printf '%s\n' "$stub"
}

# The single most important behavioural promise on the tool (spec C2): it is a
# diagnostic, so survivors are a FINDING, never a failure. Every other CLI case
# is a rejection, so without this one nothing proved the success path end to end.
# stdout is captured SEPARATELY from stderr here, rather than through bats' `run`
# (which merges them): the assertions below are about which stream carries what
# and about the FIRST LINE of the worklist, and a merged capture cannot decide
# either.
@test "survivors present still exits 0 and prints the worklist (spec C2)" {
  target="$(make_module survivors)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  rc=0
  "$SCRIPT" "$target" >"$TEST_DIR/out" 2>"$TEST_DIR/err" || rc=$?
  [ "$rc" -eq 0 ]
  out="$(cat "$TEST_DIR/out")"
  [[ "$out" == *"surviving mutants"* ]]
  [[ "$out" == *"fixture.go"* ]]
  [[ "$out" == *"L3"* ]]
  [[ "$out" == *"Replace + with -"* ]]
  # No-op dropped from the worklist AND from the count (spec O5), with the raw
  # bucket still shown so the five statuses sum (spec O7).
  [[ "$out" != *"Replace return 0 with return 0"* ]]
  [[ "$out" == *"survived 2 (1 actionable, 1 no-op)"* ]]
  # First line free of a percentage and a killed count (spec O2).
  first="$(head -1 "$TEST_DIR/out")"
  [[ "$first" != *%* ]]
  [[ "$first" != *killed* ]]
  # Paths are target-relative (spec O4/O1).
  [[ "$out" != *"$target/fixture.go"* ]]
}

@test "--json exits 0 with target-relative paths and no mutation score" {
  target="$(make_module survivorsjson)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  rc=0
  "$SCRIPT" --json "$target" >"$TEST_DIR/out.json" 2>"$TEST_DIR/err" || rc=$?
  [ "$rc" -eq 0 ]
  # stdout must be JSON and nothing else -- a machine consumer parses this.
  jq -e . "$TEST_DIR/out.json" >/dev/null
  jq -e '.survivors[0].file == "fixture.go"' "$TEST_DIR/out.json"
  jq -e '.statistics.survivedActionable == 1' "$TEST_DIR/out.json"
  jq -e '.statistics.survivedNoOp == 1' "$TEST_DIR/out.json"
  jq -e '.statistics | has("mutationScore") == false' "$TEST_DIR/out.json"
  jq -e '.buildTagsNotRun == null' "$TEST_DIR/out.json"
}

@test "aborts when the engine reports a version other than the pinned one (spec E1)" {
  target="$(make_module devengine)"
  stub="$TEST_DIR/stub-gomu-dev"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  printf 'gomu version dev\n  commit: none\n  built:  unknown\n'
  exit 0
fi
touch "$PGM_TEST_RAN_MARKER"
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  PGM_TEST_RAN_MARKER="$TEST_DIR/engine-ran"
  export PGM_TEST_RAN_MARKER
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"version mismatch"* ]]
  # And it aborted BEFORE spending a run on an unattributable engine.
  [ ! -e "$PGM_TEST_RAN_MARKER" ]
}

@test "accepts the pinned engine version and completes the analysis (spec E1)" {
  target="$(make_module pinnedengine)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  run "$SCRIPT" "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *"surviving mutants"* ]]
}

@test "aborts when the engine is missing entirely rather than reporting an empty report" {
  target="$(make_module noengine)"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/no-such-engine"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"was not found"* ]]
  [[ "$output" != *"produced no report"* ]]
}

# --tags has to reach the GUARDS, not just the engine. A package whose only test
# is behind a custom tag and FAILS is the dangerous case: without the tag in
# GOFLAGS the guard never compiles it, passes, and the engine then reads every
# mutant as KILLED -- 100%, zero survivors, exit 0.
@test "--tags reaches the tests-healthy guard, which aborts on a failing tag-gated test" {
  target="$(make_module taggedfailing)"
  rm -f "$target/fixture_test.go"
  cat >"$target/contract_test.go" <<'EOF'
//go:build contract

package fixture

import "testing"

func TestContract(t *testing.T) { t.Fatal("deliberately failing") }
EOF
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version 0.2.1\\n"; exit 0; fi\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"

  run "$SCRIPT" --tags contract "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"do not pass on unmutated source"* ]]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "a package whose only tests are tag-gated is not misreported as untested" {
  target="$(make_module taggedonly)"
  rm -f "$target/fixture_test.go"
  cat >"$target/contract_test.go" <<'EOF'
//go:build contract

package fixture

import "testing"

func TestContract(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
  # Without --tags the guard genuinely cannot see any test file.
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-unused"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version 0.2.1\\n"; exit 0; fi\n' >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"has no test files"* ]]

  # WITH --tags it must get past that guard -- the false diagnosis this fixes.
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  run "$SCRIPT" --tags contract "$target"
  [ "$status" -eq 0 ]
  [[ "$output" != *"has no test files"* ]]
}

# The scenario actually observed on this branch: a TERM sent to the top-level
# wrapper's own pid. It leaked a workdir and orphaned the engine, because the
# engine supervisor ran inside a command-substitution SUBSHELL that owned the
# trap, the workdir and the engine pid -- so the signal never reached it. This
# asserts the whole CLI, not the library function, cleans up under that signal.
@test "a TERM on the CLI's own pid kills the engine and removes the private workdir (spec CL4)" {
  target="$(make_module cliinterrupt)"

  PGM_TEST_PID_LOG="$TEST_DIR/engine.pid"
  PGM_TEST_WORKDIR_LOG="$TEST_DIR/engine.cwd"
  export PGM_TEST_PID_LOG PGM_TEST_WORKDIR_LOG

  stub="$TEST_DIR/stub-gomu-sleep"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  printf 'gomu version 0.2.1\n'
  exit 0
fi
mkdir -p "${TMPDIR:-/tmp}/gomu_overlay_$$_1"
pwd >"$PGM_TEST_WORKDIR_LOG"
printf '%s\n' "$$" >"$PGM_TEST_PID_LOG"
# `exec` so the announced pid stays valid for this process's whole life.
exec sleep 120
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"

  "$SCRIPT" "$target" >"$TEST_DIR/cli.out" 2>&1 &
  cli_pid=$!

  for _ in $(seq 1 300); do
    [ -s "$PGM_TEST_PID_LOG" ] && break
    sleep 0.1
  done
  [ -s "$PGM_TEST_PID_LOG" ]
  engine_pid="$(cat "$PGM_TEST_PID_LOG")"
  workdir="$(cat "$PGM_TEST_WORKDIR_LOG")"
  [ -d "$workdir" ]
  [ -d "$workdir/gomu_overlay_${engine_pid}_1" ]

  # The signal goes to the CLI's pid ONLY -- not to the process group -- which is
  # what made the observed leak possible.
  kill -TERM "$cli_pid"
  wait "$cli_pid" || true

  # 5s, deliberately FAR shorter than the stub's 120s sleep. A generous window
  # would let the stub simply expire on its own, at which point the orphaned
  # supervisor tidies up and the test passes without the fix -- verified: at a 30s
  # window this case passed against the un-fixed command-substitution shape.
  for _ in $(seq 1 50); do
    kill -0 "$engine_pid" 2>/dev/null || break
    sleep 0.1
  done
  run kill -0 "$engine_pid"
  [ "$status" -ne 0 ]
  [ ! -e "$workdir" ]
}

@test "a target that is not a Go module says so instead of blaming missing tests" {
  target="$TEST_DIR/notamodule"
  mkdir -p "$target"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-unused-2"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version 0.2.1\\n"; exit 0; fi\n' >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not a Go module"* ]]
  [[ "$output" != *"Write a test first"* ]]
}
