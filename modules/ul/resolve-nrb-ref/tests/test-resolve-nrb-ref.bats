#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

SCRIPT="$SCRIPTS_DIR/resolve-nrb-ref.sh"

setup() {
  TEST_DIR=$(mktemp -d)
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "prints the rev-pinned ref when the node is locked" {
  echo '{"nodes":{"phillipgreenii-nix-base":{"locked":{"rev":"deadbeef"}}}}' >"$TEST_DIR/flake.lock"
  run bash "$SCRIPT" --lock "$TEST_DIR/flake.lock"
  [ "$status" -eq 0 ]
  [ "$output" = "github:phillipgreenii/nix-repo-base/deadbeef" ]
}

@test "fails closed naming the node when the lock has no nix-repo-base node" {
  echo '{"nodes":{"other":{"locked":{"rev":"abc"}}}}' >"$TEST_DIR/flake.lock"
  run bash "$SCRIPT" --lock "$TEST_DIR/flake.lock"
  [ "$status" -ne 0 ]
  [[ $output == *"phillipgreenii-nix-base"* ]]
  [[ $output == *"nix flake lock"* ]]
  [[ $output != *"github:phillipgreenii/nix-repo-base/"* ]]
}

@test "fails closed when the node has no rev" {
  echo '{"nodes":{"phillipgreenii-nix-base":{"locked":{}}}}' >"$TEST_DIR/flake.lock"
  run bash "$SCRIPT" --lock "$TEST_DIR/flake.lock"
  [ "$status" -ne 0 ]
  [[ $output == *"phillipgreenii-nix-base"* ]]
}

@test "fails closed when the lock file is missing" {
  run bash "$SCRIPT" --lock "$TEST_DIR/nope.lock"
  [ "$status" -ne 0 ]
  [[ $output == *"nix flake lock"* ]]
}

@test "--node selects a custom node name" {
  echo '{"nodes":{"nrb":{"locked":{"rev":"cafe"}}}}' >"$TEST_DIR/flake.lock"
  run bash "$SCRIPT" --lock "$TEST_DIR/flake.lock" --node nrb
  [ "$status" -eq 0 ]
  [ "$output" = "github:phillipgreenii/nix-repo-base/cafe" ]
}
