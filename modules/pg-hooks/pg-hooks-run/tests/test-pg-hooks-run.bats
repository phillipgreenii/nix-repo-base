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
  PGH_T_DEFAULT_RUN="${BATS_TEST_DIRNAME}/../pg-hooks-run.sh"

  # nix check: BATS_SUPPORT_PATH holds the copied *.bash support files.
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../test-support}/pg-hooks-test-helper.bash"

  pgh_t_setup test-pg-hooks-run
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$PGH_T_LIB"
  cd "$GFH_REPO"
}

teardown() {
  gfh_teardown
}

# Run the runner as the stub would: PG_HOOKS_BUNDLE names the generation's
# bundle link. Extra environment goes before the stage via `env`.
_run_stage() {
  PG_HOOKS_BUNDLE="$PGH_T_COMMON/pg-hooks/gen-1/bundle" "$PGH_T_SUT" "$@"
}

_logged_args() {
  grep '^arg=' "$PGH_T_LOG" | sed 's/^arg=//'
}

_common_dir() {
  command git rev-parse --path-format=absolute --git-common-dir
}

_prek_calls() {
  if [ -f "$GFH_ROOT/prek-calls" ]; then
    wc -l <"$GFH_ROOT/prek-calls" | tr -d ' '
  else
    echo 0
  fi
}

# --- argv -------------------------------------------------------------------

@test "usage: no stage is exit 2 and --help is exit 0" {
  run --separate-stderr "$PGH_T_SUT"
  [ "$status" -eq 2 ]
  run "$PGH_T_SUT" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-hooks-run <stage>"* ]]
}

@test "runner passes exact prek argv: -q hook-impl --config <B>/prek-config.json --hook-type <stage> --hook-dir <common-dir>/hooks --script-version 4 -- args" {
  pgh_t_make_bundle
  local expected
  expected="$(printf '%s\n' -q hook-impl --config "$PGH_T_COMMON/pg-hooks/gen-1/bundle/prek-config.json" \
    --hook-type pre-commit --hook-dir "$(_common_dir)/hooks" --script-version 4 --)"
  run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$stderr" ]
  [ "$(_logged_args)" = "$expected" ]
  grep -q '^argc=11$' "$PGH_T_LOG"
}

@test "runner forwards stdin and args for pre-push (ref lines, remote name, url), pre-rebase (\$1 \$2), commit-msg, prepare-commit-msg" {
  pgh_t_make_bundle
  : >"$PGH_T_CTL/stdin"

  run _run_stage pre-push origin "https://example.invalid/r.git" <<<"refs/heads/a 1111 refs/heads/a 0000"
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 3)" = "--
origin
https://example.invalid/r.git" ]
  grep -qxF 'stdin=refs/heads/a 1111 refs/heads/a 0000' "$PGH_T_LOG"

  run _run_stage pre-rebase main feature </dev/null
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 3)" = "--
main
feature" ]
  grep -q -- '^arg=pre-rebase$' "$PGH_T_LOG"

  run _run_stage commit-msg ".git/COMMIT_EDITMSG" </dev/null
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 2)" = "--
.git/COMMIT_EDITMSG" ]

  run _run_stage prepare-commit-msg ".git/COMMIT_EDITMSG" message </dev/null
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 3)" = "--
.git/COMMIT_EDITMSG
message" ]
}

@test "runner passes arguments containing spaces intact" {
  pgh_t_make_bundle
  run _run_stage commit-msg "a path/with space" </dev/null
  [ "$status" -eq 0 ]
  [ "$(_logged_args | tail -n 1)" = "a path/with space" ]
}

@test "runner returns prek status unchanged; prints hooks-failed hint only on nonzero" {
  pgh_t_make_bundle
  run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]

  printf '7\n' >"$PGH_T_CTL/exit"
  run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 7 ]
  [ "$stderr" = "FAKE-PREK: hook output
$(pgh_msg_hooks_failed pre-commit fixture 2>&1)" ]
}

@test "runner uses bundle prek even with a decoy prek first on PATH" {
  pgh_t_make_bundle
  mkdir -p "$BATS_TEST_TMPDIR/decoy"
  printf '#!/bin/sh\necho DECOY >"%s"\nexit 0\n' "$BATS_TEST_TMPDIR/decoy-ran" >"$BATS_TEST_TMPDIR/decoy/prek"
  chmod +x "$BATS_TEST_TMPDIR/decoy/prek"
  PATH="$BATS_TEST_TMPDIR/decoy:$PATH" run _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ ! -e "$BATS_TEST_TMPDIR/decoy-ran" ]
  grep -q '^argc=11$' "$PGH_T_LOG"
}

@test "runner runs prek with git fsmonitor disabled through git's env config" {
  pgh_t_make_bundle
  run _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ "$(grep -c '^git_config=' "$PGH_T_LOG")" -eq 1 ]
  grep -qxF 'git_config=core.fsmonitor=false' "$PGH_T_LOG"
}

@test "runner appends its fsmonitor entry after the caller's own GIT_CONFIG_* entries" {
  pgh_t_make_bundle
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.name GIT_CONFIG_VALUE_0=zed run _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ "$(grep '^git_config=' "$PGH_T_LOG")" = "git_config=user.name=zed
git_config=core.fsmonitor=false" ]
}

@test "runner ignores a non-numeric GIT_CONFIG_COUNT rather than failing" {
  pgh_t_make_bundle
  GIT_CONFIG_COUNT=bogus run _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ "$(grep '^git_config=' "$PGH_T_LOG")" = "git_config=core.fsmonitor=false" ]
}

@test "runner honours GIT_INDEX_FILE and GIT_DIR from git" {
  pgh_t_track_flake_lock
  local alt="$BATS_TEST_TMPDIR/alt-index" alt_stamp
  cp "$PGH_T_COMMON/index" "$alt"
  printf '{"version":99}\n' >flake.lock
  GIT_INDEX_FILE="$alt" command git add flake.lock
  alt_stamp="$(GIT_INDEX_FILE="$alt" command git ls-files -s -- flake.lock flake.nix | command git hash-object --stdin)"
  pgh_t_make_bundle "" "$alt_stamp"
  mkdir -p "$BATS_TEST_TMPDIR/elsewhere"
  cd "$BATS_TEST_TMPDIR/elsewhere"

  # git's own environment (as during `git commit <path>`): the temporary index
  # carries the stamped content, and the repo comes from GIT_DIR, not the cwd.
  GIT_DIR="$PGH_T_COMMON" GIT_WORK_TREE="$GFH_REPO" GIT_INDEX_FILE="$alt" \
    run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  grep -q "^git_index_file=$alt\$" "$PGH_T_LOG"
  grep -q "^git_dir=$PGH_T_COMMON\$" "$PGH_T_LOG"

  # the default index (flake.lock modified but unstaged) is a different stamp
  GIT_DIR="$PGH_T_COMMON" GIT_WORK_TREE="$GFH_REPO" \
    run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [[ $stderr == *"hook bundle is stale for fixture"* ]]
}

@test "broken bundle (prek not executable) exits 12 with the broken message" {
  pgh_t_make_bundle
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 12 ]
  [ -z "$output" ]
  [ "$stderr" = "$(pgh_msg_broken fixture 'bin/prek is not executable' "$PGH_T_COMMON/pg-hooks" 2>&1)" ]
  [ "$(_prek_calls)" -eq 0 ]
}

@test "progress: one line after PG_HOOKS_PROGRESS_AFTER_S, none for a fast run" {
  pgh_t_make_bundle
  printf '2\n' >"$PGH_T_CTL/sleep"
  PG_HOOKS_PROGRESS_AFTER_S=1 run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ "$stderr" = "pg-hooks: running pre-commit hooks in fixture..." ]

  rm "$PGH_T_CTL/sleep"
  PG_HOOKS_PROGRESS_AFTER_S=1 run --separate-stderr _run_stage pre-commit
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]

  printf '2\n' >"$PGH_T_CTL/sleep"
  PG_HOOKS_PROGRESS_AFTER_S=0 run --separate-stderr _run_stage pre-commit
  [ -z "$stderr" ]
}

# --- git process budget -----------------------------------------------------

@test "at most 3 git processes for pre-commit/pre-push (rev-parse, ls-files, hash-object) and at most 1 for other stages" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  local shim="$BATS_TEST_TMPDIR/shim" counter="$BATS_TEST_TMPDIR/git-calls" stage n
  mkdir -p "$shim"
  printf '#!/bin/sh\nprintf "%%s\\n" "$*" >>"%s"\nexec "%s" "$@"\n' "$counter" "$PGH_T_REAL_GIT" >"$shim/git"
  chmod +x "$shim/git"

  for stage in pre-commit pre-push; do
    : >"$counter"
    PATH="$shim:$PATH" run _run_stage "$stage" </dev/null
    [ "$status" -eq 0 ]
    n="$(wc -l <"$counter" | tr -d ' ')"
    [ "$n" -ge 1 ]
    [ "$n" -le 3 ] || {
      cat "$counter" >&2
      return 1
    }
  done

  # the stale path stays inside the budget too
  pgh_t_make_bundle "" "0000000000000000000000000000000000000000"
  : >"$counter"
  PATH="$shim:$PATH" run --separate-stderr _run_stage pre-commit
  [[ $stderr == *"hook bundle is stale"* ]]
  n="$(wc -l <"$counter" | tr -d ' ')"
  [ "$n" -le 3 ] || {
    cat "$counter" >&2
    return 1
  }

  for stage in pre-rebase commit-msg prepare-commit-msg post-commit; do
    : >"$counter"
    PATH="$shim:$PATH" run _run_stage "$stage" </dev/null
    [ "$status" -eq 0 ]
    n="$(wc -l <"$counter" | tr -d ' ')"
    [ "$n" -le 1 ] || {
      echo "$stage used $n git processes" >&2
      cat "$counter" >&2
      return 1
    }
  done
}

# --- through the stub and real git ------------------------------------------

@test "fires at root, subdir and linked worktree" {
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  local wt="$GFH_WORK/wt" line
  mkdir -p sub
  printf 's\n' >sub/s.txt
  command git add sub/s.txt
  command git commit -q -m sub-file

  : >"$GFH_ROOT/prek-calls"
  pgh_t_commit "from root"
  [ "$(_prek_calls)" -eq 1 ]
  line="$(grep '^pwd=' "$PGH_T_LOG")"
  [[ $line == */repo ]]

  printf 'x\n' >>sub/s.txt
  command git -C sub add s.txt
  command git -C sub commit -q -m "from subdir"
  [ "$(_prek_calls)" -eq 2 ]
  line="$(grep '^pwd=' "$PGH_T_LOG")"
  [[ $line == */repo ]]

  command git worktree add -q "$wt"
  printf 'w\n' >"$wt/w.txt"
  command git -C "$wt" add w.txt
  command git -C "$wt" commit -q -m "from worktree"
  [ "$(_prek_calls)" -eq 3 ]
  line="$(grep '^pwd=' "$PGH_T_LOG")"
  [[ $line == */wt ]]
}

@test "stub works when the clone path has a space and a symlinked prefix" {
  local real="$GFH_WORK/real prefix" link="$GFH_WORK/lnk"
  mkdir -p "$real"
  ln -s "$real" "$link"
  gfh_init_repo "$real/my repo" test-pg-hooks-run
  printf 'i\n' >"$real/my repo/file.txt"
  command git -C "$real/my repo" add file.txt
  command git -C "$real/my repo" commit -q -m init
  command git -C "$real/my repo" config --unset core.hooksPath
  GFH_REPO="$link/my repo"
  PGH_T_COMMON="$GFH_REPO/.git"
  cd "$GFH_REPO"

  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  pgh_t_commit "via symlink"
  [ "$(_prek_calls)" -eq 1 ]
  grep -q '^arg=.*my repo/\.git/hooks$' "$PGH_T_LOG"
  grep -q '^arg=.*/prek-config\.json$' "$PGH_T_LOG"

  # and with no bundle the notice still names the spaced path
  rm "$PGH_T_COMMON/pg-hooks/current"
  run --separate-stderr command git -C "$GFH_REPO" commit -q --allow-empty -m nobundle
  [ "$status" -eq 0 ]
  [[ $stderr == *"Fix: (cd "*"my repo && nix run .#install-pre-commit-hooks)"* ]]
}

@test "runner works under env -i with no HOME" {
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  local p
  p="$(pgh_t_limited_path)"
  printf 'e\n' >>file.txt
  command git add file.txt
  run --separate-stderr env -i PATH="$p" GIT_CONFIG_NOSYSTEM=1 "$p/git" -C "$GFH_REPO" commit -q -m "no env"
  [ "$status" -eq 0 ] || {
    echo "$stderr" >&2
    return 1
  }
  [ "$(_prek_calls)" -eq 1 ]
  grep -qx 'home=<unset>' "$PGH_T_LOG"
  grep -qx 'arg=hook-impl' "$PGH_T_LOG"
  [[ $stderr != *"no hook bundle"* ]]
}

@test "runner works under env -i PATH=/usr/bin:/bin (outside the nix sandbox only)" {
  if [ -n "$PGH_T_SAVED_SANDBOX" ] || [ -n "$PGH_T_SAVED_SUT" ]; then
    skip "the literal /usr/bin:/bin variant runs only outside the nix sandbox"
  fi
  /usr/bin/git --version >/dev/null 2>&1 || skip "no usable /usr/bin/git"
  /usr/bin/jq --version >/dev/null 2>&1 || skip "no usable /usr/bin/jq"
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  printf 'e\n' >>file.txt
  command git add file.txt
  run --separate-stderr env -i PATH=/usr/bin:/bin GIT_CONFIG_NOSYSTEM=1 /usr/bin/git -C "$GFH_REPO" commit -q -m "no env"
  [ "$status" -eq 0 ] || {
    echo "$stderr" >&2
    return 1
  }
  [ "$(_prek_calls)" -eq 1 ]
  grep -qx 'home=<unset>' "$PGH_T_LOG"
}

@test "broken bundle through the stub blocks the commit with the rebuild line" {
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  chmod -x "$PGH_T_BUNDLE/bin/prek"
  printf 'b\n' >>file.txt
  command git add file.txt
  run --separate-stderr command git commit -q -m broken
  [ "$status" -ne 0 ]
  [[ $stderr == *"pg-hooks: hook bundle for fixture is broken (bin/prek is not executable). Rebuild: "* ]]
}

@test "stale warning printed once across 3 commits" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle "" "0000000000000000000000000000000000000000"
  pgh_t_install_stub pre-commit
  local log="$BATS_TEST_TMPDIR/commits.log" i
  : >"$log"
  for i in 1 2 3; do
    pgh_t_commit "c$i" 2>>"$log"
  done
  [ "$(_prek_calls)" -eq 3 ]
  [ "$(grep -c 'hook bundle is stale for fixture' "$log")" -eq 1 ]
}

@test "stale warning printed once across a 3-commit rebase" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle "" "0000000000000000000000000000000000000000"
  local base i
  base="$(command git rev-parse HEAD)"
  for i in 1 2 3; do
    printf 'r%s\n' "$i" >"r$i.txt"
    command git add "r$i.txt"
    command git commit -q -m "r$i"
  done
  # hooks start now, so only the rebase's own commits are counted
  pgh_t_install_stub pre-commit
  rm -f "$GFH_ROOT/prek-calls"
  local log="$BATS_TEST_TMPDIR/rebase.log"
  : >"$log"
  GIT_EDITOR=true command git rebase -q -x 'git commit --amend --no-edit -q' "$base" 2>>"$log"
  [ "$(_prek_calls)" -eq 3 ]
  [ "$(grep -c 'hook bundle is stale for fixture' "$log")" -eq 1 ]
}

@test "no stale warning for an edit outside stamp inputs, end to end" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  run --separate-stderr pgh_t_commit "plain edit"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(_prek_calls)" -eq 1 ]
}

@test "linked worktree: a private bundle wins over the shared one" {
  pgh_t_make_bundle
  pgh_t_install_stub pre-commit
  local wt="$GFH_WORK/wt" priv
  command git worktree add -q "$wt"
  priv="$(command git -C "$wt" rev-parse --path-format=absolute --absolute-git-dir)/pg-hooks"
  pgh_t_make_bundle "$priv"
  local private_store="$PGH_T_BUNDLE"
  printf 'p\n' >"$wt/p.txt"
  command git -C "$wt" add p.txt
  command git -C "$wt" commit -q -m private
  grep -qxF "arg=$priv/gen-1/bundle/prek-config.json" "$PGH_T_LOG"
  [ -d "$private_store" ]
}
