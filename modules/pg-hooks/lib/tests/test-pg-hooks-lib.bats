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
  PGH_T_SAVED_SUT=""
  PGH_T_SAVED_SANDBOX="${NIX_BUILD_TOP:-}"
  PGH_T_DEFAULT_LIB="${BATS_TEST_DIRNAME}/../pg-hooks-lib.bash"
  PGH_T_DEFAULT_RUN=""

  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../test-support}/pg-hooks-test-helper.bash"

  pgh_t_setup test-pg-hooks-lib
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$PGH_T_LIB"
  cd "$GFH_REPO"
}

teardown() {
  gfh_teardown
}

# --- pointer ----------------------------------------------------------------

@test "pointer: valid gen-N resolves" {
  mkdir -p "$BATS_TEST_TMPDIR/d"
  printf 'gen-7\n' >"$BATS_TEST_TMPDIR/d/current"
  run pgh_read_pointer "$BATS_TEST_TMPDIR/d"
  [ "$status" -eq 0 ]
  [ "$output" = "gen-7" ]
}

@test "pointer: absent -> 13" {
  mkdir -p "$BATS_TEST_TMPDIR/d"
  run pgh_read_pointer "$BATS_TEST_TMPDIR/d"
  [ "$status" -eq 13 ]
  [ -z "$output" ]
}

@test "pointer: '../x', empty, 'gen-', 'gen-1x', a directory and a symlink current -> 12" {
  local d="$BATS_TEST_TMPDIR/d" content
  mkdir -p "$d"
  for content in '../x' '' 'gen-' 'gen-1x' 'gen-1/../x' 'GEN-1'; do
    printf '%s\n' "$content" >"$d/current"
    run pgh_read_pointer "$d"
    [ "$status" -eq 12 ] || {
      echo "content '$content' gave status $status" >&2
      return 1
    }
  done
  # a zero-byte file
  : >"$d/current"
  run pgh_read_pointer "$d"
  [ "$status" -eq 12 ]
  # a symlink pointer (even to a valid pointer file) is refused
  printf 'gen-1\n' >"$BATS_TEST_TMPDIR/real-current"
  rm -f "$d/current"
  ln -s "$BATS_TEST_TMPDIR/real-current" "$d/current"
  run pgh_read_pointer "$d"
  [ "$status" -eq 12 ]
  # a dangling symlink is invalid, not absent
  rm -f "$d/current"
  ln -s "$BATS_TEST_TMPDIR/nonexistent" "$d/current"
  run pgh_read_pointer "$d"
  [ "$status" -eq 12 ]
  # a directory
  rm -f "$d/current"
  mkdir "$d/current"
  run pgh_read_pointer "$d"
  [ "$status" -eq 12 ]
}

@test "bundle: missing runner -> 13, unexecutable prek -> 12, healthy -> 0" {
  local dir="$PGH_T_COMMON/pg-hooks" b
  run pgh_bundle "$dir" gen-1
  [ "$status" -eq 13 ]
  pgh_t_make_bundle
  b="$PGH_T_BUNDLE"
  run pgh_bundle "$dir" gen-1
  [ "$status" -eq 0 ]
  [ "$output" = "$dir/gen-1/bundle" ]
  chmod -x "$b/bin/prek"
  run pgh_bundle "$dir" gen-1
  [ "$status" -eq 12 ]
  chmod +x "$b/bin/prek"
  rm "$b/bin/pg-hooks-run"
  run pgh_bundle "$dir" gen-1
  [ "$status" -eq 13 ]
}

@test "select_dir prefers private dir with current over shared" {
  local wt="$GFH_WORK/wt" common priv
  command git -C "$GFH_REPO" worktree add -q "$wt"
  common="$(command git -C "$wt" rev-parse --path-format=absolute --git-common-dir)"
  priv="$(command git -C "$wt" rev-parse --path-format=absolute --absolute-git-dir)/pg-hooks"
  cd "$wt"

  # canonical: private == shared
  run pgh_shared_dir
  [ "$output" = "$common/pg-hooks" ]
  run pgh_private_dir
  [ "$output" = "$priv" ]

  # no pointer anywhere: shared
  run pgh_select_dir
  [ "$output" = "$common/pg-hooks" ]

  # a shared pointer only: shared
  mkdir -p "$common/pg-hooks"
  printf 'gen-1\n' >"$common/pg-hooks/current"
  run pgh_select_dir
  [ "$output" = "$common/pg-hooks" ]

  # a private pointer wins
  mkdir -p "$priv"
  printf 'gen-3\n' >"$priv/current"
  run pgh_select_dir
  [ "$output" = "$priv" ]
}

@test "canonical clone: private dir equals shared dir and uses absolute paths" {
  run pgh_shared_dir
  [[ $output == /* ]]
  shared="$output"
  run pgh_private_dir
  [ "$output" = "$shared" ]
}

# --- stamp ------------------------------------------------------------------

@test "stamp equals git ls-files -s | git hash-object --stdin over the inputs" {
  pgh_t_track_flake_lock
  local expected
  expected="$(command git ls-files -s -- flake.lock flake.nix | command git hash-object --stdin)"
  run pgh_stamp flake.lock flake.nix
  [ "$status" -eq 0 ]
  [ "$output" = "$expected" ]
  [ "$output" != unknown ]
}

@test "stamp counts a staged edit but not an unstaged one" {
  pgh_t_track_flake_lock
  local before staged_expected
  before="$(pgh_stamp flake.lock flake.nix)"

  printf '{"version":8}\n' >flake.lock
  run pgh_stamp flake.lock flake.nix
  [ "$output" = "$before" ] # unstaged: the index is unchanged

  command git add flake.lock
  staged_expected="$(command git ls-files -s -- flake.lock flake.nix | command git hash-object --stdin)"
  run pgh_stamp flake.lock flake.nix
  [ "$output" = "$staged_expected" ]
  [ "$output" != "$before" ]
}

@test "stamp is unknown with no tracked inputs" {
  run pgh_stamp flake.lock flake.nix
  [ "$status" -eq 0 ]
  [ "$output" = unknown ]
}

@test "stamp follows a tracked directory input" {
  mkdir -p modules/x
  printf 'a\n' >modules/x/f
  command git add modules/x/f
  local a b
  a="$(pgh_stamp modules/x)"
  printf 'b\n' >modules/x/f
  command git add modules/x/f
  b="$(pgh_stamp modules/x)"
  [ "$a" != unknown ]
  [ "$a" != "$b" ]
}

# --- staleness --------------------------------------------------------------

# Bundle recorded against a stamp that no longer matches the index.
_stale_fixture() {
  pgh_t_track_flake_lock
  pgh_t_make_bundle "" "0000000000000000000000000000000000000000"
}

@test "stale warning printed once across 3 commits" {
  _stale_fixture
  local log="$BATS_TEST_TMPDIR/warn.log" i
  : >"$log"
  for i in 1 2 3; do
    pgh_t_commit "c$i"
    pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit 2>>"$log"
  done
  [ "$(grep -c 'hook bundle is stale' "$log")" -eq 1 ]
  grep -qF 'pg-hooks: hook bundle is stale for fixture (built 2026-10-01T00:00:00Z). Rebuild: (cd ' "$log"
  grep -qF 'nix run .#install-pre-commit-hooks)' "$log"
}

@test "stale warning marker lives in the private git dir and is keyed by stamp" {
  _stale_fixture
  pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit 2>/dev/null
  local stamp
  stamp="$(pgh_stamp flake.lock flake.nix)"
  [ -e "$PGH_T_COMMON/pg-hooks/stale-warned-$stamp" ]

  # a new stamp warns again
  printf '{"version":9}\n' >flake.lock
  command git add flake.lock
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [[ $output == *"hook bundle is stale"* ]]
}

@test "no stale warning for an edit outside stamp inputs" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle # records the current stamp
  pgh_t_commit "outside"
  printf 'more\n' >>file.txt
  command git add file.txt
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "stale warning for a staged edit of a stamp input" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  printf '{"version":8}\n' >flake.lock
  command git add flake.lock
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [[ $output == *"hook bundle is stale for fixture"* ]]
}

@test "stampPaths from meta.json join the stamp inputs" {
  pgh_t_track_flake_lock
  mkdir -p modules/hooksrc
  printf 'one\n' >modules/hooksrc/h
  command git add modules/hooksrc/h
  pgh_t_make_bundle
  jq '.stampPaths = ["modules/hooksrc"]' "$PGH_T_BUNDLE/meta.json" >"$BATS_TEST_TMPDIR/m.json"
  mv "$BATS_TEST_TMPDIR/m.json" "$PGH_T_BUNDLE/meta.json"
  # re-record the source.json stamp over the widened input set
  local stamp
  stamp="$(pgh_stamp flake.lock flake.nix modules/hooksrc)"
  jq --arg s "$stamp" '.stamp = $s' "$PGH_T_COMMON/pg-hooks/gen-1/source.json" >"$BATS_TEST_TMPDIR/s.json"
  mv "$BATS_TEST_TMPDIR/s.json" "$PGH_T_COMMON/pg-hooks/gen-1/source.json"

  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [ -z "$output" ]

  printf 'two\n' >modules/hooksrc/h
  command git add modules/hooksrc/h
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [[ $output == *"hook bundle is stale"* ]]
}

@test "stale warning only on pre-commit and pre-push" {
  _stale_fixture
  local stage
  for stage in commit-msg pre-rebase prepare-commit-msg post-commit pre-merge-commit; do
    run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 "$stage"
    [ -z "$output" ] || {
      echo "stage $stage warned: $output" >&2
      return 1
    }
  done
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-push
  [[ $output == *"hook bundle is stale"* ]]
}

@test "no stamp inputs tracked: stamp unknown, no warning" {
  pgh_t_make_bundle "" "something"
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "override head change adds (override <name> changed)" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  local other="$GFH_WORK/producer" head
  gfh_init_repo "$other" test-pg-hooks-lib
  printf 'p\n' >"$other/p.txt"
  command git -C "$other" add p.txt
  command git -C "$other" commit -q -m p1
  head="$(command git -C "$other" rev-parse HEAD)"
  jq --arg path "$other" --arg head "$head" \
    '.overrides = [{name: "producer", path: $path, head: $head, dirty: false}]' \
    "$PGH_T_COMMON/pg-hooks/gen-1/source.json" >"$BATS_TEST_TMPDIR/s.json"
  mv "$BATS_TEST_TMPDIR/s.json" "$PGH_T_COMMON/pg-hooks/gen-1/source.json"

  # nothing changed: silent
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [ -z "$output" ]

  # the override's HEAD moves: stale, naming the override
  printf 'q\n' >>"$other/p.txt"
  command git -C "$other" commit -q -am p2
  run pgh_stale_check "$PGH_T_COMMON/pg-hooks" gen-1 pre-commit
  [[ $output == *"hook bundle is stale for fixture (built 2026-10-01T00:00:00Z) (override producer changed). Rebuild: "* ]]
}

@test "plain worktree on the shared bundle with differing stamp inputs gets the worktree message" {
  pgh_t_track_flake_lock
  pgh_t_make_bundle
  local wt="$GFH_WORK/wt"
  command git -C "$GFH_REPO" worktree add -q "$wt"
  cd "$wt"
  printf '{"version":10}\n' >flake.lock
  command git add flake.lock
  # the dir a stub hands over is git's own spelling of the shared dir
  run pgh_stale_check "$(pgh_shared_dir)" gen-1 pre-commit
  [[ $output == "pg-hooks: this worktree's hook definitions differ from the shared bundle; the old hooks ran. To test your hook changes: (cd "*" && nix run .#install-pre-commit-hooks -- --private)" ]]
}

# --- messages ---------------------------------------------------------------

@test "message texts match spec 5.4 and 4.5" {
  run --separate-stderr pgh_msg_no_bundle r pre-commit /c 0 "(cd /c && nix run .#install-pre-commit-hooks)"
  [ "$stderr" = "pg-hooks: no hook bundle for r; pre-commit hooks not run. Fix: (cd /c && nix run .#install-pre-commit-hooks)" ]
  [ -z "$output" ]

  run --separate-stderr pgh_msg_no_bundle r pre-push /c 1 "FIXCMD"
  [ "$stderr" = "pg-hooks: no hook bundle for r (shared bundle lives in /c); pre-push hooks not run. Fix: FIXCMD" ]

  run --separate-stderr pgh_msg_broken r "bin/prek is not executable" "$BATS_TEST_TMPDIR/nodir"
  [[ $stderr == "pg-hooks: hook bundle for r is broken (bin/prek is not executable). Rebuild: (cd "*" && nix run .#install-pre-commit-hooks)" ]]

  run --separate-stderr pgh_msg_stale r T "REBUILD"
  [ "$stderr" = "pg-hooks: hook bundle is stale for r (built T). Rebuild: REBUILD" ]

  run --separate-stderr pgh_msg_worktree_differs "PRIV"
  [ "$stderr" = "pg-hooks: this worktree's hook definitions differ from the shared bundle; the old hooks ran. To test your hook changes: PRIV" ]

  run --separate-stderr pgh_msg_hooks_failed pre-commit r
  [ "$stderr" = "pg-hooks: pre-commit hooks failed in r. Auto-fixable formatting: run 'pg-hooks fix', then git add and commit again. Other failures: fix by hand." ]
}

@test "reinstall prints the reinstall file line, else the canonical command" {
  local dir="$PGH_T_COMMON/pg-hooks"
  mkdir -p "$dir"
  run pgh_reinstall "$dir"
  [[ $output == "(cd "*" && nix run .#install-pre-commit-hooks)" ]]
  printf '(cd /elsewhere && nix run .#install-pre-commit-hooks)\n' >"$dir/reinstall"
  run pgh_reinstall "$dir"
  [ "$output" = "(cd /elsewhere && nix run .#install-pre-commit-hooks)" ]
}

@test "exit-code constants match spec 5.3" {
  [ "$PGH_OK" -eq 0 ]
  [ "$PGH_USAGE" -eq 2 ]
  [ "$PGH_FAILED" -eq 10 ]
  [ "$PGH_SKIPPED" -eq 11 ]
  [ "$PGH_BROKEN" -eq 12 ]
  [ "$PGH_NO_BUNDLE" -eq 13 ]
  [ "$PGH_STALE" -eq 14 ]
  [ "$PGH_UNREACHABLE" -eq 15 ]
  [ "$PGH_RELOCATED" -eq 16 ]
}

# --- stub -------------------------------------------------------------------

@test "stub: no bundle prints exact notice on stderr, nothing on stdout, exit 0" {
  pgh_t_install_stub pre-commit
  local expected
  expected="$(pgh_msg_no_bundle "$(pgh_repo_name)" pre-commit 2>&1)"
  run --separate-stderr "$PGH_T_COMMON/hooks/pre-commit"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$stderr" = "$expected" ]
  [[ $stderr == "pg-hooks: no hook bundle for "* ]]
}

@test "stub: a commit with no bundle still succeeds and shows the notice" {
  pgh_t_install_stub pre-commit
  run --separate-stderr command git -C "$GFH_REPO" commit -q --allow-empty -m x
  [ "$status" -eq 0 ]
  [[ $stderr == *"pg-hooks: no hook bundle for "*"pre-commit hooks not run. Fix: "* ]]
}

@test "stub text contains no /nix/store path and the marker line" {
  pgh_t_install_stub pre-commit
  local rendered="$PGH_T_COMMON/hooks/pre-commit"
  run ! grep -q '/nix/store' "$PGH_T_STUB_TEMPLATE"
  run ! grep -q '/nix/store' "$rendered"
  [ "$(sed -n 1p "$rendered")" = "#!/bin/sh" ]
  [ "$(sed -n 2p "$rendered")" = "# managed-by: pg-hooks" ]
  run ! grep -q '@STAGE@' "$rendered"
  grep -qF "stage='pre-commit'" "$rendered"
}

@test "stub notice text equals pgh_msg_no_bundle output in a linked worktree too" {
  pgh_t_install_stub pre-push
  local wt="$GFH_WORK/wt" expected
  command git -C "$GFH_REPO" worktree add -q "$wt"
  cd "$wt"
  expected="$(pgh_msg_no_bundle "$(pgh_repo_name)" pre-push 2>&1)"
  [[ $expected == *"(shared bundle lives in "* ]]
  run --separate-stderr "$PGH_T_COMMON/hooks/pre-push"
  [ "$status" -eq 0 ]
  [ "$stderr" = "$expected" ]

  # a private reinstall file replaces the Fix line, in both renderings
  local priv
  priv="$(command git rev-parse --path-format=absolute --absolute-git-dir)/pg-hooks"
  mkdir -p "$priv"
  printf '(cd /the/worktree && nix run .#install-pre-commit-hooks -- --private)\n' >"$priv/reinstall"
  expected="$(pgh_msg_no_bundle "$(pgh_repo_name)" pre-push 2>&1)"
  [[ $expected == *"Fix: (cd /the/worktree && nix run .#install-pre-commit-hooks -- --private)" ]]
  run --separate-stderr "$PGH_T_COMMON/hooks/pre-push"
  [ "$stderr" = "$expected" ]
}

@test "stub: an invalid pointer is treated as no bundle and never blocks" {
  pgh_t_install_stub pre-commit
  mkdir -p "$PGH_T_COMMON/pg-hooks"
  printf '../escape\n' >"$PGH_T_COMMON/pg-hooks/current"
  run --separate-stderr "$PGH_T_COMMON/hooks/pre-commit"
  [ "$status" -eq 0 ]
  [[ $stderr == "pg-hooks: no hook bundle for "* ]]
}

# --- messages used by pg-hooks status ----------------------------------------

@test "pgh_msg_unreachable and pgh_msg_relocated print the spec 5.4 texts on stderr" {
  run --separate-stderr pgh_msg_unreachable /c/.git .githooks /c/.git/config /c
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: git does not run hooks from /c/.git/hooks (core.hooksPath=.githooks from /c/.git/config). Operator: git -C /c config --local --unset core.hooksPath" ]

  run --separate-stderr pgh_msg_relocated /old/place/.git "(cd /c && nix run .#install-pre-commit-hooks)"
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: this clone moved from /old/place/.git; its bundle is no longer GC-rooted. Rebuild: (cd /c && nix run .#install-pre-commit-hooks)" ]
}

@test "progress line: printed after the threshold, never when stopped first" {
  run --separate-stderr bash -c 'source "$1"; pgh_progress_start 1 "pg-hooks: working"; sleep 2; pgh_progress_stop' _ "$PGH_T_LIB"
  [ "$stderr" = "pg-hooks: working" ]

  run --separate-stderr bash -c 'source "$1"; pgh_progress_start 2 "pg-hooks: working"; pgh_progress_stop; sleep 3' _ "$PGH_T_LIB"
  [ -z "$stderr" ]
}
