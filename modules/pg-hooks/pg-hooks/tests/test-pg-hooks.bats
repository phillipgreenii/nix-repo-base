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

@test "fix is not available yet and exits 2" {
  run --separate-stderr "$PGH_T_SUT" fix
  [ "$status" -eq 2 ]
  [[ $stderr == "pg-hooks: fix is not implemented yet"* ]]
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
