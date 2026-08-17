#!/usr/bin/env bats

setup() {
  LIB_PATH="${LIB_PATH:-${BATS_TEST_DIRNAME}/../pg-go-mutate-lib.bash}"
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$LIB_PATH"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # HOME is read-only in the mkBashLibrary check's nix sandbox
  # ($HOME=/homeless-shelter), so `go` cannot create its build cache there and
  # every go list/vet/test invocation fails outright -- masking the guards
  # under test behind an unrelated environment error. Mirrors the
  # HOME/GOCACHE-under-TMPDIR convention lib/go-builders.nix already uses for
  # the same reason.
  export HOME="$TEST_DIR" GOCACHE="$TEST_DIR/go-build"
}

teardown() {
  # The interrupt test starts a long-lived stub engine. It kills it as part of
  # the assertions, but if that test fails midway the sandbox would otherwise sit
  # waiting on the leftover process.
  if [ -n "${PGM_TEST_PID_LOG:-}" ] && [ -s "${PGM_TEST_PID_LOG}" ]; then
    kill -KILL "$(cat "$PGM_TEST_PID_LOG")" 2>/dev/null || true
  fi
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
}

# Builds a module OUTSIDE any git repo, per spec CL6 and T5.
make_module() {
  local dir="$TEST_DIR/$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF'
module example.com/fixture

go 1.25
EOF
  printf 'package fixture\n\nfunc Add(a, b int) int { return a + b }\n' >"$dir/fixture.go"
  printf '%s\n' "$dir"
}

add_passing_test() {
  cat >"$1/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
}

@test "pgm_validate_flags rejects zero workers" {
  run pgm_validate_flags 0 60
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags rejects zero timeout" {
  run pgm_validate_flags 2 0
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags rejects non-numeric input" {
  run pgm_validate_flags two 60
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags accepts one and one" {
  run pgm_validate_flags 1 1
  [ "$status" -eq 0 ]
}

# Writes a stub engine whose `version` subcommand prints $1, and points
# PG_GO_MUTATE_GOMU at it. Shaped like the real thing: `gomu version` prints
# three lines and the version is on the first.
make_version_stub() {
  local reported="$1" stub="$TEST_DIR/stub-gomu-version"
  cat >"$stub" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = version ]; then
  printf 'gomu version %s\n  commit: none\n  built:  unknown\n' '$reported'
  exit 0
fi
exit 0
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"
}

@test "pgm_require_engine aborts when the engine is absent" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/no-such-engine"
  run pgm_require_engine
  [ "$status" -eq 1 ]
  [[ "$output" == *"no-such-engine"* ]]
  [[ "$output" == *"was not found"* ]]
}

@test "pgm_require_engine aborts on a dev build when a version is pinned (spec E1)" {
  make_version_stub dev
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 1 ]
  [[ "$output" == *"version mismatch"* ]]
  [[ "$output" == *dev* ]]
}

@test "pgm_require_engine accepts the pinned version" {
  make_version_stub 0.2.1
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

@test "pgm_require_engine accepts a v-prefixed report of the pinned version" {
  make_version_stub v0.2.1
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

@test "pgm_require_engine does not accept a version that merely contains the pin" {
  make_version_stub 0.2.10
  export PG_GO_MUTATE_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 1 ]
}

@test "pgm_require_engine skips the comparison when no version is pinned" {
  make_version_stub dev
  # Both seams absent -- the raw-source and bats case, which has no build-time
  # pin either (the nix check for the LIBRARY injects no mkBashScript config).
  unset PG_GO_MUTATE_GOMU_VERSION PGM_PINNED_GOMU_VERSION
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

# The gap this closes: a consumer who installs pkgs.pg-go-mutate directly sets
# NEITHER env var, so before the build-time pin existed the E1 comparison skipped
# itself on exactly the install path that also lacks the W9 store-path binding.
@test "pgm_require_engine falls back to the build-time pin when the env var is unset (spec E1)" {
  make_version_stub dev
  unset PG_GO_MUTATE_GOMU_VERSION
  # NOT exported, deliberately: the assembled script assigns it as a plain shell
  # variable (mkBashScript's `config`, not `exportedConfig`), so these cases
  # reproduce that shape rather than a stronger one. pgm_require_engine reads it
  # through bash's dynamic scoping from the sourced library, which is a separate
  # file shellcheck cannot follow from here.
  # shellcheck disable=SC2034  # read by the sourced pg-go-mutate-lib
  PGM_PINNED_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 1 ]
  [[ "$output" == *"version mismatch"* ]]
  [[ "$output" == *0.2.1* ]]
}

@test "pgm_require_engine accepts the build-time pin when the engine matches it" {
  make_version_stub 0.2.1
  unset PG_GO_MUTATE_GOMU_VERSION
  # shellcheck disable=SC2034  # ditto
  PGM_PINNED_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

# The escape hatch survives, but it is now EXPLICIT rather than a side effect of
# an unset variable -- which is what let it coincide with the unprotected install
# path in the first place.
@test "pgm_require_engine treats an empty PG_GO_MUTATE_GOMU_VERSION as a deliberate opt-out" {
  make_version_stub dev
  export PG_GO_MUTATE_GOMU_VERSION=
  # shellcheck disable=SC2034  # ditto
  PGM_PINNED_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

# The home-manager module's value is the tighter check (it is read from the
# store-path-bound package's own derivation), so it MUST win over the pin baked
# into the script at build time.
@test "pgm_require_engine prefers PG_GO_MUTATE_GOMU_VERSION over the build-time pin" {
  make_version_stub 0.3.0
  export PG_GO_MUTATE_GOMU_VERSION=0.3.0
  # shellcheck disable=SC2034  # ditto
  PGM_PINNED_GOMU_VERSION=0.2.1
  run pgm_require_engine
  [ "$status" -eq 0 ]
}

@test "pgm_has_tests reports an enumeration failure distinctly from zero tests" {
  # A directory that is not a Go module at all: `go list` fails, which must NOT
  # be reported as "no test files" (that sends the reader off to write a test
  # that is not the problem).
  target="$TEST_DIR/notamodule"
  mkdir -p "$target"
  run pgm_has_tests "$target"
  [ "$status" -eq 2 ]
  [[ "$output" == *"not a Go module"* ]]
}

@test "pgm_has_tests fails on a module with no test files" {
  target="$(make_module notests)"
  run pgm_has_tests "$target"
  [ "$status" -eq 1 ]
}

@test "pgm_has_tests succeeds when a test file exists" {
  target="$(make_module withtests)"
  add_passing_test "$target"
  run pgm_has_tests "$target"
  [ "$status" -eq 0 ]
}

@test "pgm_tests_healthy succeeds on a passing suite" {
  target="$(make_module healthy)"
  add_passing_test "$target"
  run pgm_tests_healthy "$target"
  [ "$status" -eq 0 ]
}

@test "pgm_tests_healthy fails when tests are already failing" {
  target="$(make_module failing)"
  cat >"$target/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) { t.Fatal("deliberately failing") }
EOF
  run pgm_tests_healthy "$target"
  [ "$status" -eq 1 ]
}

@test "pgm_tests_healthy fails when a test file does not compile" {
  target="$(make_module noncompiling)"
  printf 'package fixture\n\nthis is not go\n' >"$target/fixture_test.go"
  run pgm_tests_healthy "$target"
  [ "$status" -eq 1 ]
}

# `go1.1`, not `darwin`: the property under test is that ALREADY-SATISFIED
# constraints are subtracted, and `darwin` proves that only on a darwin host --
# on linux the same file is invisible to `go list` and the tag IS reported, so
# the test would assert the opposite of its own name. Every Go toolchain since
# 1.1 satisfies `go1.1`, so the portable form proves the property on every host.
@test "pgm_detect_tags ignores constraints the build context already satisfies" {
  target="$(make_module satisfiedtags)"
  add_passing_test "$target"
  printf '//go:build go1.1\n\npackage fixture\n' >"$target/plat_test.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# M-2 regression: visibility used to be matched on the BASENAME, so a tag-gated
# foo_test.go in one package counted as satisfied whenever ANY other package had
# a file of the same name -- and its tags went unreported, which is a silent
# false-gap in the worklist.
@test "pgm_detect_tags reports a tag-gated file whose basename exists in another package" {
  target="$(make_module basenamecollision)"
  add_passing_test "$target"
  mkdir -p "$target/sub"
  # Visible to go list in the ROOT package...
  printf 'package fixture\n\nimport "testing"\n\nfunc TestNoop(t *testing.T) {}\n' >"$target/shared_test.go"
  # ...and invisible, behind a custom tag, in a NESTED package of the same name.
  printf '//go:build hostile\n\npackage sub\n' >"$target/sub/shared_test.go"
  printf 'package sub\n' >"$target/sub/sub.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *hostile* ]]
}

@test "pgm_detect_tags reports an unsatisfied custom tag" {
  target="$(make_module customtags)"
  add_passing_test "$target"
  printf '//go:build contract\n\npackage fixture\n' >"$target/contract_test.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *contract* ]]
}

# Builds a `main`-package module: pgm_run_engine's main-package binary
# bookkeeping only fires for packages named "main" in `go list` output.
make_main_module() {
  local dir="$TEST_DIR/$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF'
module example.com/fixture

go 1.25
EOF
  printf 'package main\n\nfunc main() {}\n' >"$dir/main.go"
  printf '%s\n' "$dir"
}

# A canned report shaped exactly like gomu's: absolute paths, uppercase
# statuses, per-operator mutationTypes, and killedMutants deliberately 0
# (gomu only populates it in CI mode).
write_report() {
  cat >"$TEST_DIR/report.json" <<EOF
{
  "version": "0.1.0",
  "totalFiles": 1,
  "processedFiles": 1,
  "totalMutants": 5,
  "killedMutants": 0,
  "files": null,
  "duration": 1000,
  "statistics": {
    "killed": 1,
    "survived": 3,
    "timedOut": 0,
    "errors": 0,
    "notViable": 1,
    "mutationScore": 25.0,
    "mutationTypes": {
      "branch_condition": { "total": 3, "killed": 1, "survived": 2 },
      "return_zero_value": { "total": 2, "killed": 0, "survived": 1 }
    }
  },
  "results": [
    { "mutant": { "id": "$TEST_DIR/mod/a.go_0", "filePath": "$TEST_DIR/mod/a.go",
        "line": 30, "column": 5, "type": "branch_condition",
        "original": "err != nil", "mutated": "false",
        "description": "Replace branch condition \\"err != nil\\" with false" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_1", "filePath": "$TEST_DIR/mod/a.go",
        "line": 42, "column": 9, "type": "branch_condition",
        "original": "n > 0", "mutated": "true",
        "description": "Replace branch condition \\"n > 0\\" with true" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_2", "filePath": "$TEST_DIR/mod/a.go",
        "line": 50, "column": 2, "type": "return_zero_value",
        "original": "\\"\\"", "mutated": "\\"\\"",
        "description": "Replace return \\"\\" with return \\"\\"" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_3", "filePath": "$TEST_DIR/mod/a.go",
        "line": 60, "column": 2, "type": "branch_condition",
        "original": "ok", "mutated": "false",
        "description": "Replace branch condition \\"ok\\" with false" },
      "status": "KILLED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_4", "filePath": "$TEST_DIR/mod/a.go",
        "line": 70, "column": 2, "type": "return_zero_value",
        "original": "0", "mutated": "0",
        "description": "Replace return 0 with return 0" },
      "status": "NOT_VIABLE", "output": "compilation error", "error": "x" }
  ]
}
EOF
  printf '%s\n' "$TEST_DIR/report.json"
}

@test "pgm_report_sane accepts a healthy report" {
  r="$(write_report)"
  run pgm_report_sane "$r"
  [ "$status" -eq 0 ]
}

@test "pgm_report_sane rejects a missing report" {
  run pgm_report_sane "$TEST_DIR/nope.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane rejects unparseable json" {
  printf 'not json' >"$TEST_DIR/bad.json"
  run pgm_report_sane "$TEST_DIR/bad.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane rejects a null results array" {
  printf '{"totalMutants":0,"results":null,"statistics":{}}' >"$TEST_DIR/null.json"
  run pgm_report_sane "$TEST_DIR/null.json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"analyzed nothing"* ]]
}

# The fixture above trips the EARLIER `.results != null` gate, so it never
# reached this branch and the "no mutants" message was unproven. `results: []`
# is the shape that gets past that gate: the engine ran and generated nothing.
@test "pgm_report_sane rejects a zero-mutant report with its own message" {
  printf '{"totalMutants":0,"results":[],"statistics":{}}' >"$TEST_DIR/zero.json"
  run pgm_report_sane "$TEST_DIR/zero.json"
  [ "$status" -eq 1 ]
  [[ "$output" == *"generated no mutants"* ]]
}

@test "pgm_report_sane rejects a majority-non-viable report" {
  printf '{"totalMutants":10,"results":[1],"statistics":{"notViable":9,"errors":0,"killed":1,"survived":0}}' >"$TEST_DIR/nv.json"
  run pgm_report_sane "$TEST_DIR/nv.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane flags the all-killed signature as suspect" {
  printf '{"totalMutants":4,"results":[1],"statistics":{"notViable":0,"errors":0,"killed":4,"survived":0}}' >"$TEST_DIR/allk.json"
  run pgm_report_sane "$TEST_DIR/allk.json"
  [ "$status" -eq 1 ]
  [[ "$output" == *suspect* ]]
}

@test "pgm_worklist drops no-op mutants where original equals mutated" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  # The two return_zero_value no-ops must not appear.
  [[ "$output" != *"return \"\" with return \"\""* ]]
  [[ "$output" != *"L50"* ]]
  [[ "$output" != *"L70"* ]]
  # ...and the COUNT must account for them too (spec O5: dropped before the
  # worklist AND before any count). The fixture has 3 SURVIVED entries, one of
  # which is a no-op, so a bare `survived 3` would contradict the "2 surviving
  # mutants" first line with no explanation.
  [[ "$output" == *"survived 3 (2 actionable, 1 no-op)"* ]]
  [ "$(printf '%s\n' "$output" | head -1)" = "pg-go-mutate: 2 surviving mutants in $TEST_DIR/mod" ]
}

@test "pgm_worklist lists the two real survivors with line numbers" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  [[ "$output" == *"L30"* ]]
  [[ "$output" == *"L42"* ]]
  [[ "$output" != *"L60"* ]]  # KILLED
}

@test "pgm_worklist first line carries no percentage and no killed count" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  first="$(printf '%s\n' "$output" | head -1)"
  [[ "$first" != *%* ]]
  [[ "$first" != *killed* ]]
}

@test "pgm_worklist_json emits target-relative paths only" {
  r="$(write_report)"
  run pgm_worklist_json "$r" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  [[ "$output" != *"$TEST_DIR"* ]]
  printf '%s' "$output" | jq -e '.survivors[0].file == "a.go"'
}

@test "pgm_worklist_json splits the survivor count and omits the mutation score" {
  r="$(write_report)"
  run pgm_worklist_json "$r" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  # Spec O5: no-ops dropped before ANY count, so a consumer can reconcile the
  # survivors array (2) against the raw bucket (3).
  printf '%s' "$output" | jq -e '.statistics.survived == 3'
  printf '%s' "$output" | jq -e '.statistics.survivedActionable == 2'
  printf '%s' "$output" | jq -e '.statistics.survivedNoOp == 1'
  printf '%s' "$output" | jq -e '(.survivors | length) == .statistics.survivedActionable'
  # The score is deliberately not handed to a machine consumer.
  printf '%s' "$output" | jq -e 'has("mutationScore") == false'
  printf '%s' "$output" | jq -e '.statistics | has("mutationScore") == false'
}

@test "pgm_worklist_json reports build tags that were not run, and null when none" {
  r="$(write_report)"
  run pgm_worklist_json "$r" "$TEST_DIR/mod" "contract,smoke"
  [ "$status" -eq 0 ]
  printf '%s' "$output" | jq -e '.buildTagsNotRun == "contract,smoke"'

  run pgm_worklist_json "$r" "$TEST_DIR/mod" ""
  [ "$status" -eq 0 ]
  printf '%s' "$output" | jq -e '.buildTagsNotRun == null'
}

# M-7: ltrimstr is a plain prefix strip, so a target whose reported form differs
# from the logical one (darwin's /var vs /private/var) would silently ship
# absolute paths. The assertion must FAIL the run rather than emit them.
@test "pgm_worklist_json strips the resolved target form as well as the logical one" {
  # $TEST_DIR is under /var/folders on darwin; its resolved form is
  # /private/var/folders. Build a report whose filePath uses the RESOLVED form
  # while the caller passes the logical one.
  resolved="$(cd "$TEST_DIR" && pwd -P)"
  if [ "$resolved" = "$TEST_DIR" ]; then
    skip "this host's temp dir is not reached through a symlink, so the two forms coincide"
  fi
  # The target must EXIST for its resolved form to be derivable at all.
  mkdir -p "$TEST_DIR/mod"
  printf '{"totalMutants":1,"results":[{"mutant":{"id":"x","filePath":"%s/mod/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0}}' \
    "$resolved" >"$TEST_DIR/resolved.json"
  run pgm_worklist_json "$TEST_DIR/resolved.json" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  printf '%s' "$output" | jq -e '.survivors[0].file == "a.go"'
}

# Without the assertion this report renders "somewhere/else/a.go" -- a
# plausible-looking relative path that points nowhere, because the filter's
# trailing ltrimstr("/") strips the leading slash off a path it never matched.
@test "the renderers fail an absolute path that is not under the target" {
  printf '{"totalMutants":1,"results":[{"mutant":{"id":"x","filePath":"/somewhere/else/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0}}' \
    >"$TEST_DIR/foreign.json"
  run pgm_worklist_json "$TEST_DIR/foreign.json" "$TEST_DIR/mod"
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot be made target-relative"* ]]
  [[ "$output" != *"somewhere/else/a.go"* ]]

  run pgm_worklist "$TEST_DIR/foreign.json" "$TEST_DIR/mod"
  [ "$status" -eq 1 ]
  [[ "$output" == *"cannot be made target-relative"* ]]
}

# Exercises CL1 itself, not a tautology. Asserting only that the report is
# absent from the TARGET proves nothing: the stub writes into $PWD, which is
# never the target under any implementation, so deleting the private-cwd
# mechanism entirely left that assertion passing. What CL1 actually promises is
# that the engine runs in a private directory that is neither the target nor the
# caller's cwd, that the report lands THERE, and that the directory is gone
# afterwards -- so the stub records its own cwd and every part is checked.
@test "pgm_run_engine runs the engine in a private cwd and removes it (spec CL1)" {
  target="$(make_module cleanrun)"
  add_passing_test "$target"
  caller_cwd="$PWD"
  PGM_TEST_WORKDIR_LOG="$TEST_DIR/cleanrun-workdir.log"
  export PGM_TEST_WORKDIR_LOG
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: records its cwd, then writes a minimal report into it and a
# history file beside it, exactly where the real gomu writes both (relative to
# CWD, with no flag to relocate either).
pwd >"$PGM_TEST_WORKDIR_LOG"
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
printf '{}' >.gomu_history.json
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 0 ]

  # The engine's cwd was PRIVATE: not the target, not the caller's cwd.
  [ -s "$PGM_TEST_WORKDIR_LOG" ]
  workdir="$(cat "$PGM_TEST_WORKDIR_LOG")"
  [ "$workdir" != "$target" ]
  [ "$workdir" != "$caller_cwd" ]
  # ...and it is gone, along with both artifacts the engine wrote into it.
  [ ! -e "$workdir" ]
  [ ! -e "$workdir/mutation-report.json" ]
  [ ! -e "$workdir/.gomu_history.json" ]
  # The harvested report survived the cleanup, at the path the function printed.
  [ -s "$output" ]
  rm -f -- "$output"
  # Nothing landed in the target or the caller's cwd.
  [ ! -e "$target/mutation-report.json" ]
  [ ! -e "$target/.gomu_history.json" ]
  [ ! -e "$caller_cwd/mutation-report.json" ]
  [ ! -e "$caller_cwd/.gomu_history.json" ]
}

# The one case that would have caught C-2. Three separate defects met here:
# `$!` captured the background SUBSHELL rather than the engine (so every
# pid-scoped cleanup could never match), the cleanup never signalled the engine
# (so a TERM'd wrapper left it running against a deleted workdir), and gomu's
# overlay dirs leak on every path except its normal exit -- the one path the
# spec says does NOT leak.
@test "pgm_run_engine kills the engine and removes its overlay dir when TERM'd mid-run (CL3/CL4)" {
  target="$(make_module interrupted)"
  add_passing_test "$target"

  PGM_TEST_PID_LOG="$TEST_DIR/engine.pid"
  PGM_TEST_WORKDIR_LOG="$TEST_DIR/engine.cwd"
  PGM_TEST_OVERLAY_LOG="$TEST_DIR/engine.overlay"
  export PGM_TEST_PID_LOG PGM_TEST_WORKDIR_LOG PGM_TEST_OVERLAY_LOG

  stub="$TEST_DIR/stub-gomu-interrupt"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
# Stub engine: announces its own pid and cwd, creates an overlay dir named
# exactly as gomu names them (gomu_overlay_<pid>_<unixnano>, under TMPDIR),
# then becomes a sleep so the harness can TERM it mid-run. `exec sleep` keeps
# the announced pid valid for the whole life of the process -- a forked sleep
# would be a grandchild this wrapper cannot signal.
overlay="${TMPDIR:-/tmp}/gomu_overlay_$$_$(date +%s)"
mkdir -p "$overlay"
pwd >"$PGM_TEST_WORKDIR_LOG"
printf '%s\n' "$overlay" >"$PGM_TEST_OVERLAY_LOG"
printf '%s\n' "$$" >"$PGM_TEST_PID_LOG"
exec sleep 120
EOF
  chmod +x "$stub"

  # pgm_run_engine must be interrupted while it is WAITING on the engine, which
  # means the wrapper has to be a separate process this test can signal.
  runner="$TEST_DIR/runner"
  cat >"$runner" <<RUNNER
#!/usr/bin/env bash
set -euo pipefail
# shellcheck disable=SC1090
source "$LIB_PATH"
export PG_GO_MUTATE_GOMU="$stub"
pgm_run_engine "$target" 1 60 ""
RUNNER
  chmod +x "$runner"

  "$runner" >"$TEST_DIR/runner.out" 2>&1 &
  runner_pid=$!

  # Bounded wait for the engine to announce itself (no fixed sleep).
  for _ in $(seq 1 200); do
    [ -s "$PGM_TEST_PID_LOG" ] && break
    sleep 0.1
  done
  [ -s "$PGM_TEST_PID_LOG" ]
  engine_pid="$(cat "$PGM_TEST_PID_LOG")"
  overlay="$(cat "$PGM_TEST_OVERLAY_LOG")"
  workdir="$(cat "$PGM_TEST_WORKDIR_LOG")"

  # The RECORDED pid must be the engine itself. Before the `exec` fix the
  # captured pid was the subshell's, which had already exited by now -- so this
  # assertion is what proves the pid capture, and it is also the precondition
  # for the kill below being able to reach anything.
  kill -0 "$engine_pid"
  [ "$engine_pid" != "$runner_pid" ]

  # The overlay dir must live INSIDE the private workdir (TMPDIR containment),
  # which is what makes CL3 hold by construction rather than by pid arithmetic.
  [ -d "$overlay" ]
  case "$overlay" in
  "$workdir"/*) ;;
  *)
    printf 'overlay %s is not inside the private workdir %s\n' "$overlay" "$workdir" >&2
    return 1
    ;;
  esac

  kill -TERM "$runner_pid"
  wait "$runner_pid" || true

  # The engine must be dead, not orphaned. 5s, deliberately FAR shorter than the
  # stub's 120s sleep: a generous window would let the stub expire on its own and
  # the case would pass without the kill in _pgm_run_engine_cleanup.
  for _ in $(seq 1 50); do
    kill -0 "$engine_pid" 2>/dev/null || break
    sleep 0.1
  done
  run kill -0 "$engine_pid"
  [ "$status" -ne 0 ]

  # ...and the overlay dir and the private workdir must both be gone.
  [ ! -e "$overlay" ]
  [ ! -e "$workdir" ]
}

@test "pgm_run_engine removes a main-package binary it compiled that did not exist before the run" {
  target="$(make_main_module mainbin)"
  bin="$target/$(basename "$target")"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu-mainbin"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: writes a minimal report into CWD, and also drops a compiled
# binary into the target's directory, simulating gomu's compile precheck
# side effect for a `main` package (cmd.Dir is the target file's directory).
target="${@: -1}"
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
printf 'fake-binary\n' >"$target/$(basename "$target")"
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  [ ! -e "$bin" ]
  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 0 ]
  [ ! -e "$bin" ]
  # The harvested report is the CALLER's to remove (the CLI does it from its EXIT
  # trap), so a library-level test that skips this litters the ambient TMPDIR --
  # measured: 22 stale pg-go-mutate-report.*.json files accumulated in /tmp from
  # earlier local runs of this suite.
  rm -f -- "$output"
}

@test "pgm_run_engine cleans up the private workdir and returns non-zero when the harvest-time mktemp fails" {
  target="$(make_module harvestmktempfail)"
  add_passing_test "$target"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu-harvestfail"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: writes a minimal report into CWD, exactly like the other
# pgm_run_engine stubs above. The private workdir this run creates is left
# behind by the stub itself, in $PGM_TEST_WORKDIR_LOG, so the test can
# confirm it gets removed even though the harvest step below fails.
pwd >"$PGM_TEST_WORKDIR_LOG"
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  PGM_TEST_WORKDIR_LOG="$TEST_DIR/workdir.log"
  export PGM_TEST_WORKDIR_LOG

  # A `mktemp` stub that discriminates by argument shape: pgm_run_engine's
  # OWN workdir `mktemp -d` call must keep working (real mktemp), so only the
  # harvest-time `mktemp -t pg-go-mutate-report.XXXXXX.json` call is made to
  # fail -- this is the one line the hardening under test guards. Put first
  # on PATH so it shadows the real mktemp for this test only.
  real_mktemp="$(command -v mktemp)"
  mock_bin="$TEST_DIR/mock-bin"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/mktemp" <<MOCK
#!/usr/bin/env bash
for a in "\$@"; do
  case "\$a" in
  pg-go-mutate-report.*)
    echo "mock mktemp: simulated failure (no space left on device)" >&2
    exit 1
    ;;
  esac
done
exec "$real_mktemp" "\$@"
MOCK
  chmod +x "$mock_bin/mktemp"
  PATH="$mock_bin:$PATH"

  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 1 ]
  [[ "$output" == *"could not create a temp file to harvest the report"* ]]

  # The private workdir the stub recorded must be gone -- proving cleanup ran
  # rather than errexit skipping straight past it.
  [ -s "$PGM_TEST_WORKDIR_LOG" ]
  workdir="$(cat "$PGM_TEST_WORKDIR_LOG")"
  [ ! -e "$workdir" ]
}

@test "pgm_run_engine preserves a main-package binary that already existed before the run" {
  target="$(make_main_module mainbinpre)"
  bin="$target/$(basename "$target")"
  printf 'developer-build\n' >"$bin"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu-mainbinpre"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: writes a minimal report into CWD, and re-writes the
# pre-existing binary too, as a real `go build` recompile would.
target="${@: -1}"
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
printf 'recompiled\n' >"$target/$(basename "$target")"
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 0 ]
  [ -e "$bin" ]
  rm -f -- "$output"  # the harvested report is the caller's to remove
}
