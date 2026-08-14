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
