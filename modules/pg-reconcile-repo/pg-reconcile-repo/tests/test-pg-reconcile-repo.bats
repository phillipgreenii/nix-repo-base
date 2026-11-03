#!/usr/bin/env bats
# bats file_tags=type:integration
# shellcheck disable=SC1090,SC2030,SC2031

# Drives the script against REAL git: a bare "origin", a canonical clone and a
# second clone used to move origin. Nothing is mocked.

SCRIPTS_DIR="${SCRIPTS_DIR:-$(cd "$BATS_TEST_DIRNAME/.." && pwd)}"

# Wrapper that replicates the builder's composition order (.bash sourced
# before .sh) without going through a full nix build. Absolute bash path:
# /usr/bin/env is not visible in the nix sandbox.
create_wrapper() {
  {
    printf '#!%s\nset -euo pipefail\n' "$(command -v bash)"
    printf 'source "%s/pg-reconcile-repo.bash"\n' "$SCRIPTS_DIR"
    printf 'source "%s/pg-reconcile-repo.sh"\n' "$SCRIPTS_DIR"
  } >"$TEST_DIR/run_pgrr"
  chmod +x "$TEST_DIR/run_pgrr"
}

commit_file() { # repo file content
  printf '%s\n' "$3" >"$1/$2"
  command git -C "$1" add "$2"
  command git -C "$1" commit -q -m "edit $2"
}

setup() {
  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  gfh_save_env SCRIPTS_DIR
  gfh_setup test-pg-reconcile-repo
  gfh_restore_env

  TEST_DIR="$GFH_WORK"
  export TEST_DIR
  create_wrapper
  SCRIPT="$TEST_DIR/run_pgrr"

  ORIGIN="$TEST_DIR/origin.git"
  CC="$TEST_DIR/cc"
  OTHER="$TEST_DIR/other"
  gfh_init_bare "$ORIGIN"
  command git -C "$GFH_REPO" remote add origin "$ORIGIN"
  command git -C "$GFH_REPO" push -q origin main
  gfh_clone "$ORIGIN" "$CC" test-pg-reconcile-repo
  gfh_clone "$ORIGIN" "$OTHER" test-pg-reconcile-repo
  cd "$CC" || return 1
}

teardown() {
  cd / || true
  gfh_teardown
}

# Local commit in CC plus a different-file commit pushed to origin by OTHER.
diverge() {
  commit_file "$CC" local.txt local
  commit_file "$OTHER" remote.txt remote
  command git -C "$OTHER" push -q origin main
}

assert_no_leftovers() {
  [ "$(command git -C "$CC" worktree list --porcelain | grep -c "^worktree ")" -eq 1 ]
  [ -z "$(command git -C "$CC" branch --list 'pg-reconcile-repo-*')" ]
  [ ! -d "$(command git -C "$CC" rev-parse --absolute-git-dir)/rebase-merge" ]
  [ ! -d "$(command git -C "$CC" rev-parse --absolute-git-dir)/rebase-apply" ]
}

@test "--help prints usage and exits 0" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage:" ]]
}

@test "a missing REPO is a usage error" {
  run "$SCRIPT"
  [ "$status" -eq 2 ]
}

@test "up to date: reports and changes nothing" {
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 0 ]
  [ "${lines[-1]}" = "up-to-date" ]
}

@test "behind only: fast-forwards" {
  commit_file "$OTHER" remote.txt remote
  command git -C "$OTHER" push -q origin main
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 0 ]
  [[ "${lines[-1]}" == "fast-forwarded "* ]]
  [ -f "$CC/remote.txt" ]
}

@test "ahead only: reports and does not push" {
  commit_file "$CC" local.txt local
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 0 ]
  [ "${lines[-1]}" = "ahead-only" ]
  [ "$(command git -C "$ORIGIN" rev-parse main)" != "$(command git -C "$CC" rev-parse main)" ]
}

@test "diverged: rebases, swaps main, moves the working tree, cleans up" {
  diverge
  old="$(command git -C "$CC" rev-parse main)"
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 0 ]
  new="$(command git -C "$CC" rev-parse main)"
  [ "${lines[-1]}" = "reconciled $old $new" ]
  [ "$new" != "$old" ]
  # linear: origin tip is an ancestor, local commit is on top
  command git -C "$CC" merge-base --is-ancestor origin/main main
  [ "$(command git -C "$CC" rev-list --count origin/main..main)" -eq 1 ]
  [ -f "$CC/local.txt" ]
  [ -f "$CC/remote.txt" ]
  [ -z "$(command git -C "$CC" status --porcelain)" ]
  [ "$(command git -C "$CC" branch --show-current)" = main ]
  # never pushed
  [ "$(command git -C "$ORIGIN" rev-parse main)" = "$(command git -C "$CC" rev-parse origin/main)" ]
  assert_no_leftovers
}

@test "diverged: the check command runs in the rebased worktree" {
  diverge
  run "$SCRIPT" --check-cmd "test -f remote.txt" "$CC"
  [ "$status" -eq 0 ]
}

@test "check failure: exits 5 and nothing changes" {
  diverge
  old="$(command git -C "$CC" rev-parse main)"
  run "$SCRIPT" --check-cmd false "$CC"
  [ "$status" -eq 5 ]
  [ "$(command git -C "$CC" rev-parse main)" = "$old" ]
  [ ! -f "$CC/remote.txt" ]
  [ -z "$(command git -C "$CC" status --porcelain)" ]
  assert_no_leftovers
}

@test "main moved during the rebase: CAS refuses (exit 6), nothing changes" {
  diverge
  # The check command runs between the rebase and the swap; it advances the
  # canonical clone's main behind the script's back.
  cat >"$TEST_DIR/move-main.sh" <<MOVE
#!$(command -v bash)
command git -C "$CC" commit -q --allow-empty -m "moved during the rebase"
MOVE
  chmod +x "$TEST_DIR/move-main.sh"
  run "$SCRIPT" --check-cmd "$TEST_DIR/move-main.sh" "$CC"
  [ "$status" -eq 6 ]
  [ "$(command git -C "$CC" log -1 --format=%s main)" = "moved during the rebase" ]
  [ ! -f "$CC/remote.txt" ]
  [ -z "$(command git -C "$CC" status --porcelain)" ]
  assert_no_leftovers
}

@test "conflict: exits 4, nothing changes, canonical clone never mid-rebase" {
  commit_file "$CC" clash.txt local
  commit_file "$OTHER" clash.txt remote
  command git -C "$OTHER" push -q origin main
  old="$(command git -C "$CC" rev-parse main)"
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 4 ]
  [[ "$output" == *"conflict: clash.txt"* ]]
  [ "$(command git -C "$CC" rev-parse main)" = "$old" ]
  [ "$(cat "$CC/clash.txt")" = local ]
  [ -z "$(command git -C "$CC" status --porcelain)" ]
  assert_no_leftovers
}

@test "dirty canonical clone: exits 3 before touching anything" {
  diverge
  printf 'x\n' >"$CC/file.txt"
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 3 ]
  [ -d "$CC" ]
  assert_no_leftovers
}

@test "canonical clone off primary: exits 3" {
  command git -C "$CC" checkout -q -b side
  run "$SCRIPT" --no-check "$CC"
  [ "$status" -eq 3 ]
}
