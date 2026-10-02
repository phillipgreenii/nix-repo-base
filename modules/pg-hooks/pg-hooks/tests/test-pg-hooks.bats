#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

# shellcheck disable=SC2034,SC2164  # PGH_T_* are read by the sourced helper; a failed cd fails the test anyway
setup() {
  # The nix check injects these as exported variables; gfh_setup scrubs every
  # exported variable not on its allowlist, so capture them into plain locals
  # first. Locally they are empty and the relative fallbacks below apply.
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

  pgh_t_setup test-pg-hooks
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$PGH_T_LIB"
  cd "$GFH_REPO"
}

teardown() {
  gfh_teardown
}

_common_dir() {
  command git rev-parse --path-format=absolute --git-common-dir
}

_logged_args() {
  grep '^arg=' "$PGH_T_LOG" | sed 's/^arg=//'
}

# _stages_json [<bundle>]: the bundle's stages.json (the shape the pre-commit
# module emits: stage -> [{id, reason}]).
_stages_json() {
  local b=${1:-$PGH_T_BUNDLE}
  cat >"$b/stages.json" <<'JSON'
{
  "pre-commit": [
    {"id": "treefmt", "reason": "Format the tree"},
    {"id": "shellcheck", "reason": "Lint shell scripts"}
  ],
  "pre-rebase": [
    {"id": "rebase-guard", "reason": "Guard rebases"}
  ]
}
JSON
}

# _present: a bundle with a known stamp over tracked flake files.
_present() {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  _stages_json
}

_status() {
  run --separate-stderr "$PGH_T_SUT" status --porcelain
}

_field() {
  grep "^$1=" <<<"$output" | sed "s/^$1=//"
}

# --- usage -------------------------------------------------------------------

@test "no args prints usage and the common-tasks block" {
  run --separate-stderr "$PGH_T_SUT"
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-hooks"* ]]
  [[ $output == *"before committing:  git add <files>; pg-hooks fix; git commit"* ]]
  [[ $output == *"before landing:     pg-hooks run pre-land"* ]]
  [[ $output == *"diagnose:           pg-hooks status"* ]]
  [ -z "$stderr" ]
}

@test "an unknown command is exit 2 with usage on stderr" {
  run --separate-stderr "$PGH_T_SUT" frobnicate
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: unknown command: frobnicate"* ]]
}

@test "fix takes no arguments (exit 2)" {
  _present
  run --separate-stderr "$PGH_T_SUT" fix somefile
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: fix: unexpected argument: somefile"* ]]
}

# --- status ------------------------------------------------------------------

@test "status --porcelain: present exits 0 with all five lines" {
  _present
  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = present ]
  [ "$(_field bundle)" = "$(_common_dir)/pg-hooks/gen-1/bundle" ]
  [ "$(_field generation)" = gen-1 ]
  [ "$(_field stages)" = "pre-commit,pre-rebase" ]
  [ "$(_field reinstall)" = "(cd $(dirname "$(_common_dir)") && nix run .#install-pre-commit-hooks)" ]
  [ "$(printf '%s\n' "$output" | wc -l | tr -d ' ')" -eq 5 ]
  [ -z "$stderr" ]
}

@test "status --porcelain: stale exits 14" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle "$PGH_T_COMMON/pg-hooks" not-the-current-stamp
  _status
  [ "$status" -eq 14 ]
  [ "$(_field state)" = stale ]
  [ "$(_field generation)" = gen-1 ]
}

@test "status --porcelain: a staged edit to a stamp input makes it stale" {
  _present
  printf '{"version":8}\n' >"$GFH_REPO/flake.lock"
  command git add flake.lock
  _status
  [ "$status" -eq 14 ]
  [ "$(_field state)" = stale ]
}

@test "status --porcelain: missing exits 13 with empty bundle fields" {
  _status
  [ "$status" -eq 13 ]
  [ "$(_field state)" = missing ]
  [ "$(_field bundle)" = "" ]
  [ "$(_field generation)" = "" ]
  [ "$(_field stages)" = "" ]
  [[ $(_field reinstall) == *"nix run .#install-pre-commit-hooks"* ]]
}

@test "status --porcelain: a pointer to a deleted generation is missing, not broken" {
  pgh_t_make_bundle
  rm -rf "$PGH_T_COMMON/pg-hooks/gen-1/bundle"
  _status
  [ "$status" -eq 13 ]
  [ "$(_field state)" = missing ]
}

@test "status --porcelain: broken exits 12 when bin/prek is not executable" {
  _present
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  _status
  [ "$status" -eq 12 ]
  [ "$(_field state)" = broken ]
}

@test "status --porcelain: broken exits 12 on an invalid pointer" {
  pgh_t_make_bundle
  printf '../x\n' >"$PGH_T_COMMON/pg-hooks/current"
  _status
  [ "$status" -eq 12 ]
  [ "$(_field state)" = broken ]
  [ "$(_field bundle)" = "" ]
}

@test "status --porcelain: unreachable exits 15 for a local core.hooksPath=.githooks" {
  _present
  command git config core.hooksPath .githooks
  _status
  [ "$status" -eq 15 ]
  [ "$(_field state)" = unreachable ]
}

@test "status: a local core.hooksPath equal to the absolute common-dir hooks is reachable" {
  _present
  command git config core.hooksPath "$(_common_dir)/hooks"
  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = present ]
}

@test "status: a global dispatcher directory is reachable; a global path without one is not" {
  _present
  local disp="$GFH_ROOT/dispatch"
  mkdir -p "$disp"
  export GIT_CONFIG_GLOBAL="$GFH_ROOT/global-gitconfig"
  command git config --file "$GIT_CONFIG_GLOBAL" core.hooksPath "$disp"
  _status
  [ "$status" -eq 15 ]
  [ "$(_field state)" = unreachable ]

  printf '#!/bin/sh\nexit 0\n' >"$disp/pre-commit"
  chmod +x "$disp/pre-commit"
  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = present ]
}

@test "status --porcelain: relocated exits 16 when clone_path differs" {
  _present
  jq '.clone_path = "/old/place/.git"' "$PGH_T_COMMON/pg-hooks/gen-1/source.json" >"$GFH_ROOT/source.new"
  mv "$GFH_ROOT/source.new" "$PGH_T_COMMON/pg-hooks/gen-1/source.json"
  _status
  [ "$status" -eq 16 ]
  [ "$(_field state)" = relocated ]
}

@test "status --porcelain: legacy exits 0 with a usable config in the worktree" {
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = legacy ]
  [ "$(_field bundle)" = "" ]
}

@test "state precedence: relocated > unreachable > broken > stale > present" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle "$PGH_T_COMMON/pg-hooks" not-the-current-stamp
  _stages_json
  _status
  [ "$(_field state)" = stale ]

  chmod -x "$PGH_T_BUNDLE/bin/prek"
  _status
  [ "$(_field state)" = broken ]

  command git config core.hooksPath .githooks
  _status
  [ "$(_field state)" = unreachable ]

  jq '.clone_path = "/old/place/.git"' "$PGH_T_COMMON/pg-hooks/gen-1/source.json" >"$GFH_ROOT/source.new"
  mv "$GFH_ROOT/source.new" "$PGH_T_COMMON/pg-hooks/gen-1/source.json"
  _status
  [ "$(_field state)" = relocated ]
}

@test "state precedence with no bundle: unreachable, else legacy, else missing" {
  _status
  [ "$(_field state)" = missing ]

  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  _status
  [ "$(_field state)" = legacy ]

  command git config core.hooksPath .githooks
  _status
  [ "$status" -eq 15 ]
  [ "$(_field state)" = unreachable ]
}

@test "status without --porcelain prints readable lines and the exact problem message on stderr" {
  _present
  command git config core.hooksPath .githooks
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 15 ]
  [[ $output == "state:      unreachable"* ]]
  local canonical
  canonical="$(dirname "$(_common_dir)")"
  [ "$stderr" = "pg-hooks: git does not run hooks from $(_common_dir)/hooks (core.hooksPath=.githooks from $(_common_dir)/config). Operator: git -C $canonical config --local --unset core.hooksPath" ]
}

@test "status --porcelain keeps stderr quiet even for a problem state" {
  command git config core.hooksPath .githooks
  _status
  [ "$status" -eq 15 ]
  [ -z "$stderr" ]
}

@test "status outside a repo exits 2" {
  mkdir -p "$GFH_WORK/nonrepo"
  cd "$GFH_WORK/nonrepo"
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: not inside a git repository" ]]
}

@test "status in a linked worktree uses the shared bundle and a worktree-differs warning when its inputs differ" {
  _present
  command git worktree add -q "$GFH_WORK/wt" -b wt-branch
  cd "$GFH_WORK/wt"
  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = present ]

  printf '{"version":9}\n' >flake.lock
  command git add flake.lock
  run --separate-stderr "$PGH_T_SUT" status
  [ "$status" -eq 14 ]
  [[ $stderr == "pg-hooks: this worktree's hook definitions differ from the shared bundle; the old hooks ran."* ]]
}

# --- list and explain --------------------------------------------------------

@test "list prints the configured stages plus pre-land from stages.json" {
  _present
  run --separate-stderr "$PGH_T_SUT" list
  [ "$status" -eq 0 ]
  [ "$output" = "pre-commit
pre-rebase
pre-land" ]
  [ -z "$stderr" ]
}

@test "explain pre-commit lists the hooks and their reasons from stages.json" {
  _present
  run --separate-stderr "$PGH_T_SUT" explain pre-commit
  [ "$status" -eq 0 ]
  [[ $output == *"stage: pre-commit"* ]]
  [[ $output == *"triggered by: git commit"* ]]
  [[ $output == *"hooks (2):"* ]]
  [[ $output == *"  treefmt: Format the tree"* ]]
  [[ $output == *"  shellcheck: Lint shell scripts"* ]]
  [ -z "$stderr" ]
}

@test "explain pre-land describes the pre-commit hooks over the branch diff" {
  _present
  run --separate-stderr "$PGH_T_SUT" explain pre-land
  [ "$status" -eq 0 ]
  [[ $output == *"stage: pre-land"* ]]
  [[ $output == *"the pre-commit hooks over the branch diff"* ]]
  [[ $output == *"  treefmt: Format the tree"* ]]
}

@test "explain a known stage with no hooks says none configured; an unknown stage exits 2" {
  _present
  run --separate-stderr "$PGH_T_SUT" explain pre-push
  [ "$status" -eq 0 ]
  [[ $output == *"hooks: none configured"* ]]

  run --separate-stderr "$PGH_T_SUT" explain nonsense
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: explain: unknown stage: nonsense" ]]
}

@test "list and explain with no bundle print the notice and exit 13" {
  run --separate-stderr "$PGH_T_SUT" list
  [ "$status" -eq 13 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: no hook bundle for repo; list hooks not run. Fix: "* ]]
  run --separate-stderr "$PGH_T_SUT" explain pre-commit
  [ "$status" -eq 13 ]
  [ -z "$output" ]
}

@test "list on a broken bundle exits 12 with the broken message" {
  _present
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  run --separate-stderr "$PGH_T_SUT" list
  [ "$status" -eq 12 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: hook bundle for fixture is broken ("* ]]
}

# --- run ---------------------------------------------------------------------

@test "run pre-commit with no args runs prek run -c <B>/prek-config.json --hook-stage pre-commit (staged files)" {
  _present
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$stderr" ]
  [ "$(_logged_args)" = "$(printf '%s\n' -q run -c "$(_common_dir)/pg-hooks/gen-1/bundle/prek-config.json" --hook-stage pre-commit)" ]
}

@test "run --all-files passes through" {
  _present
  run --separate-stderr "$PGH_T_SUT" run pre-commit --all-files
  [ "$status" -eq 0 ]
  [ "$(_logged_args)" = "$(printf '%s\n' -q run -c "$(_common_dir)/pg-hooks/gen-1/bundle/prek-config.json" --hook-stage pre-commit --all-files)" ]
  grep -qx 'arg=--all-files' "$PGH_T_LOG"
  ! grep -qx 'arg=--files' "$PGH_T_LOG"
}

@test "run with file arguments passes --files, relative to the toplevel" {
  _present
  mkdir -p sub
  printf 'x\n' >sub/a.txt
  cd sub
  run --separate-stderr "$PGH_T_SUT" run pre-commit a.txt "../file.txt"
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 3)" = "--files
sub/a.txt
sub/../file.txt" ]
}

@test "run -- <flags> forwards raw prek flags" {
  _present
  run --separate-stderr "$PGH_T_SUT" run pre-rebase -- --verbose treefmt
  [ "$status" -eq 0 ]
  [ "$(_logged_args)" = "$(printf '%s\n' -q run -c "$(_common_dir)/pg-hooks/gen-1/bundle/prek-config.json" --hook-stage pre-rebase --verbose treefmt)" ]
  grep -qx 'arg=--hook-stage' "$PGH_T_LOG"
  grep -qx 'arg=--verbose' "$PGH_T_LOG"
}

@test "run cds to the toplevel from a subdirectory" {
  _present
  mkdir -p sub/deeper
  cd sub/deeper
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ "$(grep '^pwd=' "$PGH_T_LOG" | sed 's/^pwd=//')" = "$(cd "$GFH_REPO" && pwd -P)" ]
}

@test "run rejects files together with --all-files and unknown options (exit 2)" {
  _present
  run --separate-stderr "$PGH_T_SUT" run pre-commit --all-files a.txt
  [ "$status" -eq 2 ]
  run --separate-stderr "$PGH_T_SUT" run pre-commit --bogus
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: run: unknown option: --bogus"* ]]
}

@test "run returns exit 10 and prints the hooks-failed hint when prek fails" {
  _present
  printf '1\n' >"$PGH_T_CTL/exit"
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 10 ]
  [ "$stderr" = "FAKE-PREK: hook output
$(pgh_msg_hooks_failed pre-commit fixture 2>&1)" ]
}

@test "run prints one progress line after the threshold" {
  _present
  printf '2\n' >"$PGH_T_CTL/sleep"
  PG_HOOKS_PROGRESS_AFTER_S=1 run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ "$stderr" = "pg-hooks: running pre-commit hooks in fixture..." ]
}

@test "run with no bundle prints the notice and exits 13" {
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 13 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: no hook bundle for repo; pre-commit hooks not run. Fix: (cd $(dirname "$(_common_dir)") && nix run .#install-pre-commit-hooks)" ]
}

@test "run on a broken bundle exits 12" {
  _present
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 12 ]
  [[ $stderr == "pg-hooks: hook bundle for fixture is broken ("* ]]
}

@test "run outside a repo exits 2; an unknown stage exits 2" {
  mkdir -p "$GFH_WORK/nonrepo"
  (
    cd "$GFH_WORK/nonrepo"
    run --separate-stderr "$PGH_T_SUT" run pre-commit
    [ "$status" -eq 2 ]
    [ "$stderr" = "pg-hooks: not inside a git repository" ]
  )
  run --separate-stderr "$PGH_T_SUT" run nonsense
  [ "$status" -eq 2 ]
  [ "$stderr" = "pg-hooks: run: unknown stage: nonsense" ]
}

# --- legacy fallback (dual mode, D1) -----------------------------------------

_fake_legacy_prek() {
  mkdir -p "$GFH_ROOT/legacy-bin"
  pgh_t_write_fake_prek "$GFH_ROOT/legacy-bin/prek"
  export PATH="$GFH_ROOT/legacy-bin:$PATH"
}

@test "legacy fallback runs prek with the worktree config read in place" {
  _fake_legacy_prek
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ "$(_logged_args)" = "$(printf '%s\n' -q run -c "$(cd "$GFH_REPO" && pwd -P)/.pre-commit-config.yaml" --hook-stage pre-commit)" ]
}

@test "legacy fallback reads the canonical config in place and creates no file in the worktree" {
  _fake_legacy_prek
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  command git worktree add -q "$GFH_WORK/wt" -b wt-branch
  cd "$GFH_WORK/wt"
  [ ! -e .pre-commit-config.yaml ]
  local before
  before="$(find . -path ./.git -prune -o -print | sort)"

  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ "$(_logged_args | sed -n 4p)" = "$(cd "$GFH_REPO" && pwd -P)/.pre-commit-config.yaml" ]
  [ "$(grep '^pwd=' "$PGH_T_LOG" | sed 's/^pwd=//')" = "$(cd "$GFH_WORK/wt" && pwd -P)" ]
  [ ! -e .pre-commit-config.yaml ]
  [ ! -L .pre-commit-config.yaml ]
  [ "$(find . -path ./.git -prune -o -print | sort)" = "$before" ]

  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = legacy ]
}

@test "legacy fallback without prek on PATH exits 13" {
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  PATH="$(pgh_t_limited_path)" run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 13 ]
  [[ $stderr == *"found but prek is not on PATH" ]]
}

@test "a bundle wins over a legacy config" {
  _present
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 0 ]
  [ "$(_logged_args | sed -n 4p)" = "$(_common_dir)/pg-hooks/gen-1/bundle/prek-config.json" ]
}

# --- run pre-land ------------------------------------------------------------

@test "run pre-land passes --from-ref <primary> --to-ref HEAD" {
  _present
  command git commit -q --allow-empty -m second
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 0 ]
  [ "$(_logged_args)" = "$(printf '%s\n' -q run -c "$(_common_dir)/pg-hooks/gen-1/bundle/prek-config.json" --hook-stage pre-commit --from-ref main --to-ref HEAD)" ]
}

@test "run pre-land <ref> accepts the checked-out branch and refuses any other commit (exit 2)" {
  _present
  command git checkout -q -b feature
  command git commit -q --allow-empty -m feature-work
  run --separate-stderr "$PGH_T_SUT" run pre-land feature
  [ "$status" -eq 0 ]
  grep -qx 'arg=feature' "$PGH_T_LOG"

  run --separate-stderr "$PGH_T_SUT" run pre-land main
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: run pre-land refuses main: it is not the commit checked out in this worktree"* ]]
}

@test "primary resolution order: pgii-integrate-branch.primaryBranch, origin/HEAD, main" {
  _present
  command git commit -q --allow-empty -m second
  command git branch develop
  command git branch trunk
  command git symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/develop

  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 0 ]
  [ "$(_logged_args | sed -n '/^--from-ref$/{n;p;}')" = develop ]

  command git config pgii-integrate-branch.primaryBranch trunk
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 0 ]
  [ "$(_logged_args | sed -n '/^--from-ref$/{n;p;}')" = trunk ]

  command git config --unset pgii-integrate-branch.primaryBranch
  command git symbolic-ref --delete refs/remotes/origin/HEAD
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 0 ]
  [ "$(_logged_args | sed -n '/^--from-ref$/{n;p;}')" = main ]
}

@test "run pre-land refuses an unresolvable primary branch (exit 2)" {
  _present
  command git config pgii-integrate-branch.primaryBranch nope
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: run pre-land: cannot resolve the primary branch: nope" ]]
}

@test "run pre-land takes no files and no --all-files (exit 2)" {
  _present
  run --separate-stderr "$PGH_T_SUT" run pre-land --all-files
  [ "$status" -eq 2 ]
  run --separate-stderr "$PGH_T_SUT" run pre-land HEAD extra
  [ "$status" -eq 2 ]
}

@test "run pre-land with no bundle exits 13; with a legacy config it runs that config" {
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 13 ]
  [[ $stderr == "pg-hooks: no hook bundle for repo; pre-land hooks not run. Fix: "* ]]

  _fake_legacy_prek
  printf 'repos: []\n' >"$GFH_REPO/.pre-commit-config.yaml"
  run --separate-stderr "$PGH_T_SUT" run pre-land
  [ "$status" -eq 0 ]
  grep -qx 'arg=--from-ref' "$PGH_T_LOG"
}

# --- stdout/stderr discipline and odd paths ----------------------------------

@test "messages go to stderr; stdout carries only status, list and explain output" {
  run --separate-stderr "$PGH_T_SUT" run pre-commit
  [ "$status" -eq 13 ]
  [ -z "$output" ]
  [ -n "$stderr" ]
  [[ $stderr == pg-hooks:* ]]

  run --separate-stderr "$PGH_T_SUT" run nonsense
  [ -z "$output" ]
  [[ $stderr == pg-hooks:* ]]
}

@test "paths with spaces and a symlinked prefix" {
  local real="$GFH_WORK/sp ace/real" link="$GFH_WORK/link"
  mkdir -p "$GFH_WORK/sp ace"
  gfh_init_repo "$real/repo" test-pg-hooks
  command git -C "$real/repo" config --unset core.hooksPath
  printf 'x\n' >"$real/repo/file.txt"
  command git -C "$real/repo" add file.txt
  command git -C "$real/repo" commit -q -m initial
  ln -s "$GFH_WORK/sp ace" "$link"

  cd "$link/real/repo"
  PGH_T_COMMON="$link/real/repo/.git"
  pgh_t_make_bundle "$PGH_T_COMMON/pg-hooks"
  _stages_json

  _status
  [ "$status" -eq 0 ]
  [ "$(_field state)" = present ]
  [[ "$(_field bundle)" == *"sp ace/real/repo/.git/pg-hooks/gen-1/bundle" ]]

  mkdir -p "a dir"
  printf 'x\n' >"a dir/f g.txt"
  cd "a dir"
  run --separate-stderr "$PGH_T_SUT" run pre-commit "f g.txt"
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 1)" = "a dir/f g.txt" ]
  [ "$(grep '^pwd=' "$PGH_T_LOG" | sed 's/^pwd=//')" = "$(cd "$real/repo" && pwd -P)" ]
}

# --- fix ---------------------------------------------------------------------
#
# Stand-in fixers live in the fake bundle's bin/ (they are NOT on PATH), log to
# $GFH_ROOT/fixer.log, and are described by a fake fixers.json.

# _mk_fixer <name> <sed-script>: a fixer that applies <sed-script> to each
# argument in place and logs "<name> <argv>" (one line per invocation).
_mk_fixer() {
  local name=$1 script=$2
  {
    printf '#!/bin/sh\n'
    printf 'printf '\''%%s %%s\\n'\'' '\''%s'\'' "$*" >>'\''%s'\''\n' "$name" "$GFH_ROOT/fixer.log"
    printf 'for f in "$@"; do\n'
    printf '  sed '\''%s'\'' "$f" >"$f.fixtmp" && cat "$f.fixtmp" >"$f"\n' "$script"
    printf '  rm -f "$f.fixtmp"\n'
    printf 'done\n'
    printf 'exit 0\n'
  } >"$PGH_T_BUNDLE/bin/$name"
  chmod +x "$PGH_T_BUNDLE/bin/$name"
}

# _fixers <json>: write fixers.json into the fake bundle; @BIN@ is replaced
# with the bundle's bin directory.
_fixers() {
  printf '%s\n' "${1//@BIN@/$PGH_T_BUNDLE/bin}" >"$PGH_T_BUNDLE/fixers.json"
}

# _one_fixer: the common case, a single fixer turning "bad" into "good".
_one_fixer() {
  _mk_fixer fixbad 's/bad/good/g'
  _fixers '[{"name":"fixbad","command":"@BIN@/fixbad","mode":"files","includes":[],"excludes":[]}]'
}

# _stage <path>...: write "bad" into each file and git add it.
_stage() {
  local f
  for f in "$@"; do
    mkdir -p "$(dirname "$f")"
    printf 'bad\n' >"$f"
    command git add -- "$f"
  done
}

_fixer_log() { cat "$GFH_ROOT/fixer.log" 2>/dev/null || true; }

_staged_content() { command git show ":$1"; }

@test "fix fixes and restages only staged files" {
  _present
  _one_fixer
  _stage a.txt
  printf 'bad\n' >b.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$stderr" ]
  [ "$(cat a.txt)" = good ]
  [ "$(_staged_content a.txt)" = good ]
  [ "$(_fixer_log)" = "fixbad a.txt" ]
  # the fixer lives in the bundle, not on PATH
  ! command -v fixbad
}

@test "fix leaves untracked and unstaged files untouched" {
  _present
  _one_fixer
  printf 'bad\n' >>file.txt
  printf 'bad\n' >untracked.txt
  _stage a.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log)" = "fixbad a.txt" ]
  grep -q bad file.txt
  [ "$(cat untracked.txt)" = bad ]
  [ "$(command git status --porcelain)" = "A  a.txt
 M file.txt
?? untracked.txt" ]
}

@test "fix skips a file with staged and unstaged changes, lists every skipped file, exit 11" {
  _present
  _one_fixer
  _stage s1.txt "s two.txt" g.txt
  printf 'more\n' >>s1.txt
  printf 'more\n' >>"s two.txt"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 11 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: fix skipped s two.txt: it has staged and unstaged changes. Run: git add s\\ two.txt && pg-hooks fix (or git restore --staged s\\ two.txt)
pg-hooks: fix skipped s1.txt: it has staged and unstaged changes. Run: git add s1.txt && pg-hooks fix (or git restore --staged s1.txt)" ]
  # the clean file was fixed and restaged; the skipped ones are untouched
  [ "$(cat g.txt)" = good ]
  [ "$(_staged_content g.txt)" = good ]
  [ "$(_fixer_log)" = "fixbad g.txt" ]
  [ "$(_staged_content s1.txt)" = bad ]
  [ "$(cat s1.txt)" = "bad
more" ]
}

@test "fix refuses during a merge (exit 2)" {
  _present
  _one_fixer
  command git checkout -q -b other
  printf 'o\n' >other.txt
  command git add other.txt
  command git commit -q -m other
  command git checkout -q main
  command git merge -q --no-commit --no-ff other
  [ -e "$(command git rev-parse --absolute-git-dir)/MERGE_HEAD" ]
  _stage a.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: fix refused: a merge is in progress"* ]]
  [ "$(cat a.txt)" = bad ]
  [ -z "$(_fixer_log)" ]
}

@test "fix refuses during a rebase or a cherry-pick (exit 2)" {
  _present
  _one_fixer
  _stage a.txt
  local gd
  gd=$(command git rev-parse --absolute-git-dir)
  mkdir "$gd/rebase-merge"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: fix refused: a rebase is in progress"* ]]
  rmdir "$gd/rebase-merge"
  : >"$gd/CHERRY_PICK_HEAD"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: fix refused: a cherry-pick is in progress"* ]]
  [ "$(cat a.txt)" = bad ]
}

@test "fix runs fixers in fixers.json order" {
  _present
  _mk_fixer first 's/bad/bad/'
  _mk_fixer second 's/bad/bad/'
  _mk_fixer inserted 's/bad/bad/'
  _fixers '[
    {"name":"first","command":"@BIN@/first","mode":"files","includes":[],"excludes":[]},
    {"name":"inserted","command":"@BIN@/inserted","mode":"files","includes":[],"excludes":[]},
    {"name":"second","command":"@BIN@/second","mode":"files","includes":[],"excludes":[]}
  ]'
  _stage a.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log)" = "first a.txt
inserted a.txt
second a.txt" ]
}

@test "fix per-file mode calls a fixer once per staged .nix file" {
  _present
  _mk_fixer statix 's/bad/good/'
  _fixers '[{"name":"statix","command":"@BIN@/statix fix","mode":"per-file","includes":["*.nix"],"excludes":[]}]'
  _stage a.nix sub/b.nix c.nix d.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log | sort)" = "statix fix a.nix
statix fix c.nix
statix fix sub/b.nix" ]
  [ "$(cat sub/b.nix)" = good ]
  [ "$(cat d.txt)" = bad ]
}

@test "fix accepts an argv array command and passes the arguments through" {
  _present
  _mk_fixer ruff 's/bad/good/'
  _fixers '[{"name":"ruff","command":["@BIN@/ruff","check","--fix"],"mode":"files","includes":["*.py"],"excludes":[]}]'
  _stage a.py
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log)" = "ruff check --fix a.py" ]
  [ "$(_staged_content a.py)" = good ]
}

@test "fix never adds an unsafe-fixes flag to a ruff command" {
  _present
  _mk_fixer ruff 's/bad/good/'
  _fixers '[{"name":"ruff","command":"@BIN@/ruff check --fix","mode":"files","includes":["*.py"],"excludes":[]}]'
  _stage a.py b.py
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log)" = "ruff check --fix a.py b.py" ]
  ! _fixer_log | grep -q -e '--unsafe-fixes'
}

@test "fix runs treefmt to convergence within 5 passes; a second run is a no-op" {
  _present
  # One hop per invocation: step1 -> step2 -> step3 -> done.
  _mk_fixer treefmt 's/step3/done/;s/step2/step3/;s/step1/step2/'
  _fixers '[{"name":"treefmt","command":"@BIN@/treefmt","mode":"files","includes":[],"excludes":[]}]'
  printf 'step1\n' >a.md
  command git add a.md
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(cat a.md)" = "done" ]
  [ "$(_staged_content a.md)" = "done" ]
  [ "$(_fixer_log | wc -l | tr -d ' ')" = 4 ]
  local before
  before=$(command git ls-files -s)
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(command git ls-files -s)" = "$before" ]
  [ "$(_fixer_log | wc -l | tr -d ' ')" = 5 ]
}

@test "fix caps treefmt at 5 passes and says so when it does not converge" {
  _present
  _mk_fixer treefmt 's/$/x/'
  _fixers '[{"name":"treefmt","command":"@BIN@/treefmt","mode":"files","includes":[],"excludes":[]}]'
  _stage a.md
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log | wc -l | tr -d ' ')" = 5 ]
  [ "$stderr" = "pg-hooks: fixer treefmt did not converge in 5 passes; the files may change again on the next run." ]
}

@test "fix applies the repo excludes as a grep -E regex and fixer includes/excludes as globs" {
  _present
  _mk_fixer mdfix 's/bad/good/'
  _fixers '[{"name":"mdfix","command":"@BIN@/mdfix","mode":"files","includes":["*.md"],"excludes":["docs/skip/*"]}]'
  printf '{"exclude":"(^_sources/|[.]lock$)"}\n' >"$PGH_T_BUNDLE/prek-config.json"
  _stage a.md docs/skip/b.md docs/c.md d.txt _sources/e.md x.lock
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log | tr ' ' '\n' | sort | tr '\n' ' ')" = "a.md docs/c.md mdfix " ]
  [ "$(cat _sources/e.md)" = bad ]
  [ "$(cat docs/skip/b.md)" = bad ]
  [ "$(cat d.txt)" = bad ]
}

@test "fix batches at most 200 paths per invocation" {
  _present
  _one_fixer
  local i
  for i in $(seq 1 450); do
    printf 'bad\n' >"f$i.txt"
  done
  command git add -- f*.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log | awk '{print NF - 1}' | tr '\n' ' ')" = "200 200 50 " ]
  [ "$(command git diff --name-only | wc -l | tr -d ' ')" = 0 ]
  [ "$(_staged_content f450.txt)" = good ]
}

@test "fix handles paths with spaces and runs from a subdirectory" {
  _present
  _one_fixer
  _stage "a dir/f g.txt"
  cd "a dir"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$(_fixer_log)" = "fixbad a dir/f g.txt" ]
  [ "$(_staged_content "a dir/f g.txt")" = good ]
}

@test "fix reports a failing fixer with its name, files and full output, exit 10" {
  _present
  printf '#!/bin/sh\necho "boom line one"\necho "boom line two" >&2\nexit 3\n' >"$PGH_T_BUNDLE/bin/boom"
  chmod +x "$PGH_T_BUNDLE/bin/boom"
  _mk_fixer after 's/bad/good/'
  _fixers '[
    {"name":"boom","command":"@BIN@/boom","mode":"files","includes":[],"excludes":[]},
    {"name":"after","command":"@BIN@/after","mode":"files","includes":[],"excludes":[]}
  ]'
  _stage x.txt y.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 10 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: fixer boom failed on x.txt y.txt (exit 3); its output follows:
boom line one
boom line two
Fix by hand, git add the file(s), then rerun pg-hooks fix." ]
  [ -z "$(_fixer_log)" ]
}

@test "fix treats a fixer that exits 1 after fixing a file as success" {
  _present
  {
    printf '#!/bin/sh\nrc=0\n'
    printf 'for f in "$@"; do\n'
    printf '  if grep -q bad "$f"; then\n'
    printf '    sed '\''s/bad/good/'\'' "$f" >"$f.fixtmp" && cat "$f.fixtmp" >"$f"\n'
    printf '    rm -f "$f.fixtmp"\n    rc=1\n  fi\ndone\nexit "$rc"\n'
  } >"$PGH_T_BUNDLE/bin/fixes-and-fails"
  chmod +x "$PGH_T_BUNDLE/bin/fixes-and-fails"
  _fixers '[{"name":"eof","command":"@BIN@/fixes-and-fails","mode":"files","includes":[],"excludes":[]}]'
  _stage a.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(_staged_content a.txt)" = good ]
}

@test "fix appends one timings.jsonl line per fixer that ran" {
  _present
  _mk_fixer one 's/bad/good/'
  _mk_fixer two 's/x/x/'
  _mk_fixer skipped 's/x/x/'
  _fixers '[
    {"name":"one","command":"@BIN@/one","mode":"files","includes":[],"excludes":[]},
    {"name":"two","command":"@BIN@/two","mode":"files","includes":[],"excludes":[]},
    {"name":"skipped","command":"@BIN@/skipped","mode":"files","includes":["*.nomatch"],"excludes":[]}
  ]'
  _stage a.txt b.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  local t="$HOME/.local/state/pg-hooks/timings.jsonl"
  [ "$(wc -l <"$t" | tr -d ' ')" = 2 ]
  jq -e -s '
    all(.[]; (keys | sort) == ["exit", "files", "seconds", "tool"]
      and (.files | type == "number") and (.seconds | type == "number") and (.exit == 0))
    and (map(.tool) == ["one", "two"]) and (.[0].files == 2)' "$t" >/dev/null
  # a second run appends (does not truncate)
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$(wc -l <"$t" | tr -d ' ')" = 4 ]
}

@test "fix honours XDG_STATE_HOME and tolerates an unwritable state dir" {
  _present
  _one_fixer
  _stage a.txt
  export XDG_STATE_HOME="$GFH_ROOT/xdg"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -s "$GFH_ROOT/xdg/pg-hooks/timings.jsonl" ]
  # a regular file where the directory should be: mkdir fails, the run does not
  : >"$GFH_ROOT/notadir"
  export XDG_STATE_HOME="$GFH_ROOT/notadir"
  _stage b.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(_staged_content b.txt)" = good ]
}

@test "fix prints one progress line after the threshold and none below it" {
  _present
  printf '#!/bin/sh\nsleep 2\n' >"$PGH_T_BUNDLE/bin/slow"
  chmod +x "$PGH_T_BUNDLE/bin/slow"
  _fixers '[{"name":"slow","command":"@BIN@/slow","mode":"files","includes":[],"excludes":[]}]'
  _stage a.txt
  PG_HOOKS_PROGRESS_AFTER_S=1 run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ "$stderr" = "pg-hooks: running fixers in fixture..." ]
  PG_HOOKS_PROGRESS_AFTER_S=99 run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
}

@test "fix with no bundle prints the notice and exits 13" {
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 13 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: no hook bundle for repo; fix hooks not run. Fix: (cd $(dirname "$(_common_dir)") && nix run .#install-pre-commit-hooks)" ]
}

@test "fix on a bundle without a usable fixers.json exits 12" {
  _present
  _stage a.txt
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 12 ]
  [[ $stderr == "pg-hooks: hook bundle for fixture is broken (fixers.json is missing or invalid)"* ]]
}

@test "fix with nothing staged exits 0 silently; outside a repo exits 2" {
  _present
  _one_fixer
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 0 ]
  [ -z "$output$stderr" ]
  mkdir -p "$GFH_WORK/nonrepo"
  cd "$GFH_WORK/nonrepo"
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 2 ]
  [[ $stderr == pg-hooks:* ]]
}
