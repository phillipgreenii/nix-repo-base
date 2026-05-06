#!/usr/bin/env bats

# Tests for pn-workspace-tree script

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

LIB_PATH="${LIB_PATH:-$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash}"

run_script() {
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-workspace-tree.sh'" -- "$@"
}

# Three-repo workspace fixture:
#   terminal-flake  (no inputName — terminal)
#   repo-base       (inputName: nix-base, leaf)
#   repo-mid        (inputName: nix-mid, depends on nix-base via follows + nixpkgs)
#
# Workspace-only tree (alphabetical):
#   terminal-flake
#   ├── repo-base
#   └── repo-mid
#       └── repo-base [↑ shown above]
#
# --all-inputs adds nixpkgs (n < r, so nixpkgs sorts first):
#   terminal-flake
#   ├── nixpkgs
#   ├── repo-base
#   └── repo-mid
#       ├── nixpkgs [↑ shown above]
#       └── repo-base [↑ shown above]

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR
  export REAL_HOME="$HOME"
  setup_test_home

  FIXTURE_LOCK='{
    "nodes": {
      "root": {"inputs": {"nix-base": "nix-base", "nix-mid": "nix-mid", "nixpkgs": "nixpkgs"}},
      "nix-base": {"inputs": {}},
      "nix-mid": {"inputs": {"nix-base": ["nix-base"], "nixpkgs": ["nixpkgs"]}},
      "nixpkgs": {"inputs": {}}
    },
    "root": "root",
    "version": 7
  }'
  export FIXTURE_LOCK

  mkdir -p "$TEST_DIR/workspace/terminal-flake"
  mkdir -p "$TEST_DIR/workspace/repo-base"
  mkdir -p "$TEST_DIR/workspace/repo-mid"

  cat >"$TEST_DIR/workspace/pn-workspace.toml" <<'TOML'
apply_command = "sudo darwin-rebuild switch"
use_lock = true
TOML

  cat >"$TEST_DIR/workspace/pn-workspace.lock" <<LOCK
[
  {"path": "terminal-flake"},
  {"path": "repo-base", "inputName": "nix-base"},
  {"path": "repo-mid", "inputName": "nix-mid"}
]
LOCK

  touch "$TEST_DIR/workspace/terminal-flake/flake.nix"
  touch "$TEST_DIR/workspace/repo-base/flake.nix"
  touch "$TEST_DIR/workspace/repo-mid/flake.nix"

  echo "$FIXTURE_LOCK" >"$TEST_DIR/workspace/terminal-flake/flake.lock"

  export PN_WORKSPACE_ROOT="$TEST_DIR/workspace"
}

teardown() {
  assert_no_real_paths_touched
  rm -rf "$TEST_DIR"
}

@test "--help exits 0 and shows Usage" {
  run run_script --help
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "-h exits 0 and shows Usage" {
  run run_script -h
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "Usage"
}

@test "unknown flag exits 1 with error" {
  run run_script --not-a-flag
  [ "$status" -eq 1 ]
  echo "$output" | grep -q "error: unknown option"
}

@test "--root with nonexistent dir exits nonzero" {
  run run_script --root /nonexistent/path/xyz
  [ "$status" -ne 0 ]
}
