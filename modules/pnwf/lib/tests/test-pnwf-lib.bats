#!/usr/bin/env bats

# Test suite for pnwf-lib: guarded git/pn primitives shared by every pnwf
# subcommand. The review-critical property under test is that every
# exit-code-as-boolean git probe survives a REAL `set -euo pipefail` caller —
# see the H1 harness tests below, which run the probe inside a fresh
# `bash -euo pipefail -c '...'` subprocess rather than bats' own (non -e)
# shell. Merely sourcing the lib into bats' shell would prove nothing.

setup() {
  if [[ -z ${LIB_PATH:-} ]]; then
    # Local dev: source from source directory
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)/pnwf-lib.bash"
  fi

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Mocks live OUTSIDE the repo working tree (sibling dir), never inside it.
  MOCK_DIR="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_DIR"
  PATH="$MOCK_DIR:$PATH"
  export PATH MOCK_DIR

  # Default integrate-branch-support mock: called bare (no --json flag), it
  # emits JSON unconditionally, mirroring the real tool.
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"

  REPO="$TEST_DIR/repo"
  mkdir -p "$REPO"
  command git -C "$REPO" init -q -b main
  command git -C "$REPO" config user.email "test@example.com"
  command git -C "$REPO" config user.name "Test"
  echo one >"$REPO/file.txt"
  command git -C "$REPO" add file.txt
  command git -C "$REPO" commit -q -m "initial"
  export REPO
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- pnwf_branch_exists -------------------------------------------------

@test "pnwf_branch_exists: existing branch returns true" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_branch_exists '$REPO' feature"
  [ "$status" -eq 0 ]
}

@test "pnwf_branch_exists: missing branch (non-zero) does not abort caller" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_branch_exists '$REPO' nope; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_is_ancestor_of_primary ---------------------------------------

@test "pnwf_is_ancestor_of_primary: landed branch classifies as landed" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "landed" ]
}

@test "pnwf_is_ancestor_of_primary: not-landed branch (exit 1) does not abort" {
  command git -C "$REPO" checkout -q -b feature
  echo two >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "second"
  command git -C "$REPO" checkout -q main
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
  [ "$output" = "not-landed" ]
}

@test "pnwf_is_ancestor_of_primary: absent ref (exit 128) does not abort" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_is_ancestor_of_primary '$REPO' does-not-exist main"
  [ "$status" -eq 0 ]
  [ "$output" = "absent" ]
}

# --- pnwf_worktree_present ----------------------------------------------

@test "pnwf_worktree_present: true when a worktree is checked out for the branch" {
  command git -C "$REPO" branch feature
  command git -C "$REPO" worktree add -q "$TEST_DIR/feature-wt" feature
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_worktree_present '$REPO' feature"
  [ "$status" -eq 0 ]
}

@test "pnwf_worktree_present: false (non-zero) for a branch with no worktree does not abort" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_worktree_present '$REPO' feature; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_working_tree_dirty ---------------------------------------------

@test "pnwf_working_tree_dirty: clean tree returns false without aborting" {
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_working_tree_dirty '$REPO'; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

@test "pnwf_working_tree_dirty: untracked file marks tree dirty" {
  echo extra >"$REPO/untracked.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO'"
  [ "$status" -eq 0 ]
}

@test "pnwf_working_tree_dirty: modified tracked file marks tree dirty" {
  echo changed >"$REPO/file.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_working_tree_dirty '$REPO'"
  [ "$status" -eq 0 ]
}

# --- pnwf_ahead_of_primary ------------------------------------------------

@test "pnwf_ahead_of_primary: branch with extra commits is ahead" {
  command git -C "$REPO" checkout -q -b feature
  echo two >"$REPO/file.txt"
  command git -C "$REPO" commit -q -am "second"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_ahead_of_primary '$REPO' feature main"
  [ "$status" -eq 0 ]
}

@test "pnwf_ahead_of_primary: branch identical to primary is not ahead (does not abort)" {
  command git -C "$REPO" branch feature
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_ahead_of_primary '$REPO' feature main; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_canonical_on_primary_and_clean ----------------------------------

@test "pnwf_canonical_on_primary_and_clean: true when on primary and clean" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_canonical_on_primary_and_clean '$REPO' main"
  [ "$status" -eq 0 ]
}

@test "pnwf_canonical_on_primary_and_clean: false on a different branch does not abort" {
  command git -C "$REPO" checkout -q -b feature
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_canonical_on_primary_and_clean '$REPO' main; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

@test "pnwf_canonical_on_primary_and_clean: false when dirty does not abort" {
  echo extra >"$REPO/untracked.txt"
  run bash -euo pipefail -c "source '$LIB_PATH'; if pnwf_canonical_on_primary_and_clean '$REPO' main; then echo yes; else echo no; fi"
  [ "$status" -eq 0 ]
  [ "$output" = "no" ]
}

# --- pnwf_resolve_primary_branch ------------------------------------------

@test "pnwf_resolve_primary_branch: relays a non-default primary_branch (trunk)" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"trunk","strategy":null}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_resolve_primary_branch '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "trunk" ]
}

@test "pnwf_resolve_primary_branch: relays default main" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_resolve_primary_branch '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "main" ]
}

# --- pnwf_strategy ---------------------------------------------------------

@test "pnwf_strategy: relays a declared strategy" {
  cat >"$MOCK_DIR/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":"ff-merge-to-main"}'
MOCK
  chmod +x "$MOCK_DIR/integrate-branch-support"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_strategy '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "ff-merge-to-main" ]
}

@test "pnwf_strategy: defaults to the string null when absent" {
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_strategy '$REPO'"
  [ "$status" -eq 0 ]
  [ "$output" = "null" ]
}

# --- pnwf_topo_order -------------------------------------------------------

@test "pnwf_topo_order: reads order from a fixture set lock" {
  lock_file="$TEST_DIR/set-lock.json"
  printf '{"order":["repoA","repoB","repoC"]}' >"$lock_file"
  run bash -euo pipefail -c "source '$LIB_PATH'; pnwf_topo_order '$lock_file'"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "repoA" ]
  [ "${lines[1]}" = "repoB" ]
  [ "${lines[2]}" = "repoC" ]
}
