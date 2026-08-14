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

@test "pgm_detect_tags ignores satisfied constraints like darwin and linux" {
  target="$(make_module satisfiedtags)"
  add_passing_test "$target"
  printf '//go:build darwin\n\npackage fixture\n' >"$target/plat_test.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
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

@test "pgm_run_engine leaves no artifacts in the target directory" {
  target="$(make_module cleanrun)"
  add_passing_test "$target"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: writes a minimal report into CWD, as the real gomu does.
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 0 ]
  [ ! -e "$target/mutation-report.json" ]
  [ ! -e "$target/.gomu_history.json" ]
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
}
