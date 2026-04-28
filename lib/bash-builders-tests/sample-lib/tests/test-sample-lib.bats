#!/usr/bin/env bats

# Test suite for sample-lib

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  if [[ -d "${LIB_PATH}" ]]; then
    # LIB_PATH is a directory (local dev) — source the .bash file by name
    source "${LIB_PATH}/sample-lib.bash"
  else
    # LIB_PATH is a file (nix composed library) — source directly
    source "${LIB_PATH}"
  fi
}

@test "sample_greet returns greeting" {
  run sample_greet "World"
  [ "$status" -eq 0 ]
  [ "$output" = "Hello, World!" ]
}

@test "sample_greet with spaces in name" {
  run sample_greet "Jane Doe"
  [ "$status" -eq 0 ]
  [ "$output" = "Hello, Jane Doe!" ]
}

@test "sample_greet fails without argument" {
  run sample_greet
  [ "$status" -ne 0 ]
}

@test "sample_add returns sum" {
  run sample_add 2 3
  [ "$status" -eq 0 ]
  [ "$output" = "5" ]
}

@test "sample_add handles negative numbers" {
  run sample_add -1 5
  [ "$status" -eq 0 ]
  [ "$output" = "4" ]
}

@test "sample_add fails without arguments" {
  run sample_add
  [ "$status" -ne 0 ]
}
