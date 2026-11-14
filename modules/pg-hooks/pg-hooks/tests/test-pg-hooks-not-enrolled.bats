#!/usr/bin/env bats
# bats file_tags=type:unit

# The not-enrolled state, the pg-hooks.enabled opt-out and the hooks= key of
# `pg-hooks status --porcelain`, against the real tool and a real git fixture.

bats_require_minimum_version 1.5.0

# shellcheck disable=SC2034,SC2164  # PGH_T_* are read by the sourced helper; a failed cd fails the test anyway
setup() {
  PGH_T_SAVED_TS="${TEST_SUPPORT:-}"
  PGH_T_SAVED_LIB="${LIB_PATH:-}"
  PGH_T_SAVED_SUT="${SCRIPT_UNDER_TEST:-}"
  PGH_T_SAVED_SANDBOX="${NIX_BUILD_TOP:-}"
  PGH_T_DEFAULT_LIB="${BATS_TEST_DIRNAME}/../../lib/pg-hooks-lib.bash"
  PGH_T_DEFAULT_RUN="${BATS_TEST_DIRNAME}/../pg-hooks.sh"

  # shellcheck disable=SC1091  # runtime-resolved path
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../test-support}/pg-hooks-test-helper.bash"

  pgh_t_setup test-pg-hooks-not-enrolled
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$PGH_T_LIB"
  cd "$GFH_REPO"
}

teardown() {
  gfh_teardown
}

# _st [<dir>]: porcelain status from <dir> (default: the fixture repo).
_st() {
  local d=${1:-$GFH_REPO}
  run --separate-stderr bash -c 'cd "$1" && "$2" status --porcelain' _ "$d" "$PGH_T_SUT"
}

_field() {
  grep "^$1=" <<<"$output" | sed "s/^$1=//"
}

# _expect <state> <exit> <hooks>
_expect() {
  [ "$status" -eq "$2" ]
  [ "$(_field state)" = "$1" ]
  [ "$(_field hooks)" = "$3" ]
}

_valid_bundle() {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  printf '{"pre-commit":[{"id":"treefmt","reason":"fmt"}]}\n' >"$PGH_T_BUNDLE/stages.json"
}

_relocate() {
  jq '.clone_path = "/old/place/.git"' "$PGH_T_COMMON/pg-hooks/gen-1/source.json" >"$GFH_ROOT/source.new"
  mv "$GFH_ROOT/source.new" "$PGH_T_COMMON/pg-hooks/gen-1/source.json"
}

_prek_calls() {
  if [ -e "$GFH_ROOT/prek-calls" ]; then wc -l <"$GFH_ROOT/prek-calls" | tr -d ' '; else echo 0; fi
}

# --- automatic detection ------------------------------------------------------

@test "no flake, no bundle, reachable hooks: not-enrolled, exit 0, hooks=n/a, one NOT-run line" {
  _st
  _expect not-enrolled 0 n/a
  [ "$stderr" = "" ]
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 0 ]
  [ "$stderr" = "pg-hooks: NOT run: no flake.nix in this clone, so pg-hooks does not apply. Silence: git config pg-hooks.enabled false." ]
  [[ $output == *"state:      not-enrolled"* ]]
}

@test "flake at root, no bundle: missing, 13, skipped" {
  pgh_t_track_flake_lock
  _st
  _expect missing 13 skipped
}

@test "flake in one immediate subdirectory, no bundle: missing, 13, skipped" {
  mkdir -p "$GFH_REPO/nix"
  printf '{ }\n' >"$GFH_REPO/nix/flake.nix"
  command git add nix/flake.nix
  command git commit -q -m "nix flake"
  _st
  _expect missing 13 skipped
}

@test "linked worktree on a branch without the flake, no bundle: missing, 13, skipped" {
  pgh_t_track_flake_lock
  command git worktree add -q "$GFH_WORK/wt" -b noflake
  command git -C "$GFH_WORK/wt" rm -q flake.nix flake.lock
  command git -C "$GFH_WORK/wt" commit -q -m "drop the flake"
  [ ! -e "$GFH_WORK/wt/flake.nix" ]
  _st "$GFH_WORK/wt"
  _expect missing 13 skipped
}

@test "bare repo: not-enrolled, exit 0, hooks=n/a" {
  gfh_init_bare "$GFH_WORK/bare.git"
  # the harness disables hooks with core.hooksPath=/dev/null; that would read as unreachable
  command git -C "$GFH_WORK/bare.git" config --unset core.hooksPath
  _st "$GFH_WORK/bare.git"
  _expect not-enrolled 0 n/a
}

@test "no flake, no bundle, stale core.hooksPath: unreachable, 15, skipped" {
  command git config core.hooksPath /nonexistent/old-clone/.git/hooks
  _st
  _expect unreachable 15 skipped
}

# --- hooks= for the bundle states ---------------------------------------------

@test "valid bundle: present, 0, runs; stale, 14, runs; relocated, 16, runs" {
  _valid_bundle
  _st
  _expect present 0 runs
  _relocate
  _st
  _expect relocated 16 runs
  pgh_t_make_bundle "$PGH_T_COMMON/pg-hooks" not-the-current-stamp
  _st
  _expect stale 14 runs
}

@test "broken: an invalid pointer is skipped, a bin/prek that is not executable is failing" {
  _valid_bundle
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  _st
  _expect broken 12 failing
  chmod +x "$PGH_T_BUNDLE/bin/prek"
  printf 'bogus\n' >"$PGH_T_COMMON/pg-hooks/current"
  _st
  _expect broken 12 skipped
}

# --- the opt-out flag ---------------------------------------------------------

@test "flag false outranks a valid bundle, a relocated bundle and a stale core.hooksPath" {
  _valid_bundle
  command git config pg-hooks.enabled false
  _st
  _expect not-enrolled 0 n/a
  [ "$(_field bundle)" = "" ]
  [ "$(_field generation)" = "" ]
  [ "$(_field stages)" = "" ]
  [ "$(_field reinstall)" = "" ]
  _relocate
  _st
  _expect not-enrolled 0 n/a
  command git config core.hooksPath /nonexistent/old-clone/.git/hooks
  _st
  _expect not-enrolled 0 n/a
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 0 ]
  [ "$stderr" = "pg-hooks: NOT run: this clone opted out (pg-hooks.enabled=false). Re-enable: git config --unset pg-hooks.enabled" ]
}

@test "flag true with no flake is still not-enrolled" {
  command git config pg-hooks.enabled true
  _st
  _expect not-enrolled 0 n/a
}

@test "flag true with a valid bundle changes nothing" {
  _valid_bundle
  command git config pg-hooks.enabled true
  _st
  _expect present 0 runs
}

@test "flag spellings False, 0, no, off all opt out" {
  _valid_bundle
  local v
  for v in False 0 no off; do
    command git config pg-hooks.enabled "$v"
    _st
    _expect not-enrolled 0 n/a
  done
}

@test "an invalid flag value is treated as unset: warning in human mode only, porcelain stderr quiet" {
  _valid_bundle
  command git config pg-hooks.enabled maybe
  _st
  _expect present 0 runs
  [ -z "$stderr" ]
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 0 ]
  [[ $stderr == *"pg-hooks.enabled is not a valid boolean"* ]]
  [[ $stderr != *"fatal"* ]]
}

# --- where the flag can live --------------------------------------------------

@test "flag in config.worktree applies" {
  _valid_bundle
  command git config extensions.worktreeConfig true
  command git config --worktree pg-hooks.enabled false
  _st
  _expect not-enrolled 0 n/a
}

@test "flag in the global config applies" {
  _valid_bundle
  git config --file "$HOME/.gitconfig" pg-hooks.enabled false
  _st
  _expect not-enrolled 0 n/a
}

@test "includeIf gitdir: flag applies to the clone and a linked worktree, not to a clone outside the condition" {
  _valid_bundle
  local real
  real="$(cd "$GFH_REPO" && pwd -P)"
  printf '[pg-hooks]\n\tenabled = false\n' >"$GFH_WORK/optout.inc"
  printf '[includeIf "gitdir:%s/"]\n\tpath = %s\n' "$real" "$GFH_WORK/optout.inc" >"$HOME/.gitconfig"
  command git worktree add -q "$GFH_WORK/wt" -b inc-wt
  _st
  _expect not-enrolled 0 n/a
  _st "$GFH_WORK/wt"
  _expect not-enrolled 0 n/a
  gfh_init_repo "$GFH_WORK/other" test-pg-hooks-not-enrolled
  command git -C "$GFH_WORK/other" config --unset core.hooksPath
  _st "$GFH_WORK/other"
  _expect not-enrolled 0 n/a # no flake there: not-enrolled by detection
  [ "$(cd "$GFH_WORK/other" && git config --type=bool --get pg-hooks.enabled || echo unset)" = unset ]
  printf '{ }\n' >"$GFH_WORK/other/flake.nix"
  command git -C "$GFH_WORK/other" add flake.nix
  command git -C "$GFH_WORK/other" commit -q -m flake
  _st "$GFH_WORK/other"
  _expect missing 13 skipped
}

# --- the other commands --------------------------------------------------------

@test "run, fix, list, explain when not enrolled: one NOT-run line, exit 0, prek never invoked" {
  local c
  for c in "run pre-commit" "fix" "list" "explain pre-commit" "run pre-land"; do
    # shellcheck disable=SC2086  # $c is a word list on purpose
    run --separate-stderr "$PGH_T_SUT" $c
    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [ "$stderr" = "pg-hooks: NOT run: no flake.nix in this clone, so pg-hooks does not apply. Silence: git config pg-hooks.enabled false." ]
  done
  [ "$(_prek_calls)" -eq 0 ]
}

@test "run, fix, list, explain with the flag false and a valid bundle: opted-out line, exit 0, prek never invoked" {
  _valid_bundle
  command git config pg-hooks.enabled false
  local c
  for c in "run pre-commit" "fix" "list" "explain pre-commit" "run pre-land"; do
    # shellcheck disable=SC2086  # $c is a word list on purpose
    run --separate-stderr "$PGH_T_SUT" $c
    [ "$status" -eq 0 ]
    [ -z "$output" ]
    [ "$stderr" = "pg-hooks: NOT run: this clone opted out (pg-hooks.enabled=false). Re-enable: git config --unset pg-hooks.enabled" ]
  done
  [ "$(_prek_calls)" -eq 0 ]
}

@test "with a flake and no bundle the explicit commands still exit 13" {
  pgh_t_track_flake_lock
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 13 ]
  [[ $stderr == "pg-hooks: no hook bundle for "* ]]
}

# --- porcelain shape -----------------------------------------------------------

@test "porcelain: six keys with hooks= last, for present and for not-enrolled" {
  _valid_bundle
  _st
  [ "$(printf '%s\n' "$output" | cut -d= -f1 | paste -sd, -)" = "state,bundle,generation,stages,reinstall,hooks" ]
  command git config pg-hooks.enabled false
  _st
  [ "$(printf '%s\n' "$output" | cut -d= -f1 | paste -sd, -)" = "state,bundle,generation,stages,reinstall,hooks" ]
  [ "$(printf '%s\n' "$output" | tail -n 1)" = "hooks=n/a" ]
}
