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
  PGH_T_DEFAULT_RUN="${BATS_TEST_DIRNAME}/../pg-hooks-install.sh"

  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  # shellcheck disable=SC1091  # runtime-resolved path (nix: BATS_SUPPORT_PATH)
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../test-support}/pg-hooks-test-helper.bash"

  pgh_t_setup test-pg-hooks-install
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$PGH_T_LIB"

  # The raw-source runs read the stub template from the environment; the
  # assembled script (nix) has it baked in and ignores this.
  export STUB_TEMPLATE="$PGH_T_STUB_TEMPLATE"

  # A fake nix-store: records its argv and creates the out-link (the --add-root
  # path) to the realised path, like `nix-store --add-root --indirect --realise`.
  FAKE_BIN="$GFH_ROOT/fakebin"
  FAKE_LOG="$GFH_ROOT/nix-store.log"
  mkdir -p "$FAKE_BIN"
  cat >"$FAKE_BIN/nix-store" <<EOF
#!/bin/sh
printf '%s\\n' "\$*" >>'$FAKE_LOG'
[ "\$1" = --add-root ] && [ "\$3" = --indirect ] && [ "\$4" = --realise ] || exit 64
ln -s "\$5" "\$2"
EOF
  chmod +x "$FAKE_BIN/nix-store"
  export PG_HOOKS_NIX_STORE_BIN="$FAKE_BIN/nix-store"

  CANON="$(cd "$GFH_REPO" && pwd -P)"
  COMMON="$CANON/.git"
  SHARED="$COMMON/pg-hooks"
  cd "$GFH_REPO"
}

teardown() {
  gfh_teardown
}

# _mk_bundle [<stages.json>]: a fixture bundle with the files the installer
# reads (stages.json, meta.json) and the executables it checks. Sets B.
_mk_bundle() {
  local stages=${1:-'{"pre-commit":[{"id":"treefmt","reason":"treefmt"}]}'}
  B="$GFH_ROOT/store/bundle-$RANDOM"
  mkdir -p "$B/bin"
  printf '#!/bin/sh\nexit 0\n' >"$B/bin/prek"
  printf '#!/bin/sh\necho "ran $1"\n' >"$B/bin/pg-hooks-run"
  chmod +x "$B/bin/prek" "$B/bin/pg-hooks-run"
  printf '%s\n' "$stages" >"$B/stages.json"
  printf '{"repo":"fixture","stampPaths":[],"stages":["pre-commit"]}\n' >"$B/meta.json"
}

# _install [args...]: run the installer from the fixture repo.
# shellcheck disable=SC2120  # the arguments are optional
_install() {
  (cd "$GFH_REPO" && "$PGH_T_SUT" --bundle "$B" "$@")
}

# _cur: the generation `current` names in the shared dir.
_cur() {
  cat "$SHARED/current"
}

# _add_worktree <name>: a linked worktree of the fixture repo; sets WT and WTGIT.
_add_worktree() {
  WT="$GFH_ROOT/wt-$1"
  command git -C "$GFH_REPO" worktree add -q -b "wt-$1" "$WT"
  WT="$(cd "$WT" && pwd -P)"
  WTGIT="$COMMON/worktrees/wt-$1"
}

_head_of() {
  command git -C "$1" rev-parse HEAD
}

# --- usage -------------------------------------------------------------------

@test "usage: --help exits 0; no --bundle, an unknown option and a bad --override exit 2" {
  _mk_bundle
  run "$PGH_T_SUT" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-hooks-install --bundle"* ]]
  run --separate-stderr _install_nobundle
  [ "$status" -eq 2 ]
  [[ $stderr == *"--bundle <path> is required"* ]]
  run --separate-stderr _install --nope
  [ "$status" -eq 2 ]
  run --separate-stderr _install --override noequals
  [ "$status" -eq 2 ]
  [ ! -e "$SHARED" ]
}

_install_nobundle() {
  (cd "$GFH_REPO" && "$PGH_T_SUT")
}

@test "outside a repository exits 2; a bundle without stages.json exits 1" {
  _mk_bundle
  run --separate-stderr bash -c "cd '$GFH_ROOT' && '$PGH_T_SUT' --bundle '$B'"
  [ "$status" -eq 2 ]
  [[ $stderr == *"not inside a git repository"* ]]
  rm "$B/stages.json"
  run --separate-stderr _install
  [ "$status" -eq 1 ]
  [[ $stderr == *"no stages.json"* ]]
}

# --- canonical install -------------------------------------------------------

@test "canonical install writes current, reinstall, gen-1/bundle and gen-1/source.json" {
  _mk_bundle
  pgh_t_track_flake_lock
  run --separate-stderr _install
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$stderr" ]
  [ "$(_cur)" = gen-1 ]
  [ "$(cat "$SHARED/reinstall")" = "(cd $CANON && nix run .#install-pre-commit-hooks)" ]
  [ -L "$SHARED/gen-1/bundle" ]
  [ "$(readlink "$SHARED/gen-1/bundle")" = "$B" ]
  [ -x "$SHARED/gen-1/bundle/bin/pg-hooks-run" ]
  local want_stamp
  want_stamp="$(cd "$GFH_REPO" && pgh_stamp flake.lock flake.nix)"
  [ "$(jq -r .stamp "$SHARED/gen-1/source.json")" = "$want_stamp" ]
  [ "$(jq -c .overrides "$SHARED/gen-1/source.json")" = "[]" ]
  [ "$(jq -r .clone_path "$SHARED/gen-1/source.json")" = "$COMMON" ]
  [[ $(jq -r .built_at "$SHARED/gen-1/source.json") =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]
  [ -x "$COMMON/hooks/pre-commit" ]
}

@test "stamp covers the bundle's stampPaths and is unknown when no input is tracked" {
  _mk_bundle
  run _install
  [ "$status" -eq 0 ]
  [ "$(jq -r .stamp "$SHARED/gen-1/source.json")" = unknown ]
  printf '{"repo":"fixture","stampPaths":["extra.txt"],"stages":["pre-commit"]}\n' >"$B/meta.json"
  printf 'x\n' >"$GFH_REPO/extra.txt"
  command git -C "$GFH_REPO" add extra.txt
  run _install
  [ "$status" -eq 0 ]
  [ "$(jq -r .stamp "$SHARED/gen-2/source.json")" = "$(cd "$GFH_REPO" && pgh_stamp flake.lock flake.nix extra.txt)" ]
}

@test "add-root argv is --add-root <gen-N>/bundle --indirect --realise <bundle>" {
  _mk_bundle
  run _install
  [ "$status" -eq 0 ]
  [ "$(cat "$FAKE_LOG")" = "--add-root $SHARED/gen-1/bundle --indirect --realise $B" ]
  run _install
  [ "$(tail -n 1 "$FAKE_LOG")" = "--add-root $SHARED/gen-2/bundle --indirect --realise $B" ]
}

@test "a failing nix-store leaves no generation, no pointer and exits 1" {
  _mk_bundle
  printf '#!/bin/sh\nexit 7\n' >"$FAKE_BIN/nix-store"
  run --separate-stderr _install
  [ "$status" -eq 1 ]
  [[ $stderr == *"nix-store --add-root failed"* ]]
  [ ! -e "$SHARED/current" ]
  [ ! -e "$SHARED/gen-1" ]
}

@test "current is a regular file, not a symlink, and is replaced by rename" {
  _mk_bundle
  run _install
  [ "$status" -eq 0 ]
  [ -f "$SHARED/current" ]
  [ ! -L "$SHARED/current" ]
  # A second name for the old inode keeps the old content only if the new
  # pointer arrived by rename(2) rather than by rewriting the file in place.
  ln "$SHARED/current" "$SHARED/held"
  run _install
  [ "$status" -eq 0 ]
  [ "$(cat "$SHARED/held")" = gen-1 ]
  [ "$(_cur)" = gen-2 ]
  [ ! -L "$SHARED/current" ]
}

@test "a legacy symlink pointer is replaced by a regular file" {
  _mk_bundle
  mkdir -p "$SHARED/gen-1"
  ln -s gen-1 "$SHARED/current"
  run _install
  [ "$status" -eq 0 ]
  [ -f "$SHARED/current" ]
  [ ! -L "$SHARED/current" ]
  [ "$(_cur)" = gen-2 ]
}

@test "generation claim retries on EEXIST" {
  _mk_bundle
  # A mkdir shim that plays a racing peer: the first claim of gen-1 is taken by
  # someone else just before our mkdir runs, so the real mkdir fails EEXIST.
  local real_mkdir
  real_mkdir="$(command -v mkdir)"
  mkdir -p "$GFH_ROOT/shim"
  cat >"$GFH_ROOT/shim/mkdir" <<EOF
#!/bin/sh
case "\$1" in
*/pg-hooks/gen-1) [ -e '$GFH_ROOT/raced' ] || { : >'$GFH_ROOT/raced'; '$real_mkdir' "\$1"; } ;;
esac
exec '$real_mkdir' "\$@"
EOF
  chmod +x "$GFH_ROOT/shim/mkdir"
  PATH="$GFH_ROOT/shim:$PATH" run _install
  [ "$status" -eq 0 ]
  [ -e "$GFH_ROOT/raced" ]
  [ "$(_cur)" = gen-2 ]
  [ -L "$SHARED/gen-2/bundle" ]
  [ -f "$SHARED/gen-2/source.json" ]
}

@test "three concurrent installs leave a valid current and intact generations" {
  _mk_bundle
  local pids=() i
  for i in 1 2 3; do
    _install &
    pids+=($!)
  done
  for i in "${pids[@]}"; do
    wait "$i"
  done
  local gen d
  gen="$(pgh_read_pointer "$SHARED")"
  [ -x "$SHARED/$gen/bundle/bin/pg-hooks-run" ]
  [ -f "$SHARED/$gen/source.json" ]
  for d in "$SHARED"/gen-*; do
    [ -L "$d/bundle" ]
    [ -x "$d/bundle/bin/pg-hooks-run" ]
    jq -e . "$d/source.json" >/dev/null
  done
  [ -x "$COMMON/hooks/pre-commit" ]
}

@test "pruning keeps current and the previous generation only, and leaves stale-warned markers alone" {
  _mk_bundle
  mkdir -p "$SHARED"
  : >"$SHARED/stale-warned-abc123"
  local i
  for i in 1 2 3 4; do
    run _install
    [ "$status" -eq 0 ]
  done
  [ "$(_cur)" = gen-4 ]
  [ -d "$SHARED/gen-4" ]
  [ -d "$SHARED/gen-3" ]
  [ ! -e "$SHARED/gen-2" ]
  [ ! -e "$SHARED/gen-1" ]
  [ -f "$SHARED/stale-warned-abc123" ]
  [ -f "$SHARED/reinstall" ]
}

@test "pruning leaves incomplete generations alone (a concurrent install may own them)" {
  _mk_bundle
  run _install
  run _install
  # Peers' in-flight claims (no source.json yet).
  mkdir -p "$SHARED/gen-9" "$SHARED/gen-0"
  run _install
  [ "$status" -eq 0 ]
  [ -d "$SHARED/gen-9" ]
  [ -d "$SHARED/gen-0" ]
}

# --- linked worktrees and --private -------------------------------------------

@test "linked worktree without --private is refused with the canonical path in the message (exit 2)" {
  _mk_bundle
  _add_worktree a
  run --separate-stderr bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B'"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == "pg-hooks: install refused in a linked worktree; the shared bundle belongs to $CANON. Run there: (cd $CANON && nix run .#install-pre-commit-hooks), or add --private for a worktree-only bundle." ]]
  [ ! -e "$SHARED" ]
  [ ! -e "$WTGIT/pg-hooks" ]
  [ ! -e "$COMMON/hooks/pre-commit" ]
}

@test "--private writes only under the worktree git dir and may add a missing stub" {
  _mk_bundle
  _add_worktree a
  run --separate-stderr bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ ! -e "$SHARED" ]
  [ "$(cat "$WTGIT/pg-hooks/current")" = gen-1 ]
  [ -L "$WTGIT/pg-hooks/gen-1/bundle" ]
  [ "$(jq -r .clone_path "$WTGIT/pg-hooks/gen-1/source.json")" = "$COMMON" ]
  [ "$(cat "$WTGIT/pg-hooks/reinstall")" = "(cd $WT && nix run .#install-pre-commit-hooks -- --private)" ]
  # The stage had no stub yet, so the set's new stage works.
  [ -x "$COMMON/hooks/pre-commit" ]
  grep -qx '# managed-by: pg-hooks' "$COMMON/hooks/pre-commit"
}

@test "--private never rewrites an existing stub or a foreign file" {
  _mk_bundle '{"pre-commit":[{"id":"a","reason":"a"}],"pre-push":[{"id":"b","reason":"b"}]}'
  _add_worktree a
  mkdir -p "$COMMON/hooks"
  printf '#!/bin/sh\n# managed-by: pg-hooks\n# my custom stub\n' >"$COMMON/hooks/pre-commit"
  chmod +x "$COMMON/hooks/pre-commit"
  printf '#!/bin/sh\necho mine\n' >"$COMMON/hooks/pre-push"
  chmod +x "$COMMON/hooks/pre-push"
  local before_commit before_push
  before_commit="$(cat "$COMMON/hooks/pre-commit")"
  before_push="$(cat "$COMMON/hooks/pre-push")"
  run --separate-stderr bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(cat "$COMMON/hooks/pre-commit")" = "$before_commit" ]
  [ "$(cat "$COMMON/hooks/pre-push")" = "$before_push" ]
}

@test "--private never removes an own stub for a stage without hooks" {
  _mk_bundle
  _add_worktree a
  mkdir -p "$COMMON/hooks"
  printf '#!/bin/sh\n# managed-by: pg-hooks\n' >"$COMMON/hooks/pre-push"
  run bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private"
  [ "$status" -eq 0 ]
  [ -f "$COMMON/hooks/pre-push" ]
}

@test "--private in the canonical clone is a plain canonical install" {
  _mk_bundle
  run _install --private
  [ "$status" -eq 0 ]
  [ "$(_cur)" = gen-1 ]
  [ "$(cat "$SHARED/reinstall")" = "(cd $CANON && nix run .#install-pre-commit-hooks)" ]
}

@test "git worktree remove deletes the private dir and leaves the shared dir" {
  _mk_bundle
  run _install
  [ "$status" -eq 0 ]
  _add_worktree a
  run bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private"
  [ "$status" -eq 0 ]
  [ -d "$WTGIT/pg-hooks" ]
  command git -C "$GFH_REPO" worktree remove --force "$WT"
  [ ! -e "$WTGIT/pg-hooks" ]
  [ -d "$SHARED" ]
  [ "$(_cur)" = gen-1 ]
}

# --- overrides ---------------------------------------------------------------

@test "--override name=path records head and dirty in source.json" {
  _mk_bundle
  local dirty_repo="$GFH_ROOT/ov-dirty" clean_repo="$GFH_ROOT/ov-clean"
  gfh_init_repo "$dirty_repo" ov
  gfh_init_repo "$clean_repo" ov
  printf 'a\n' >"$dirty_repo/f"
  printf 'b\n' >"$clean_repo/f"
  command git -C "$dirty_repo" add f
  command git -C "$dirty_repo" commit -q -m one
  command git -C "$clean_repo" add f
  command git -C "$clean_repo" commit -q -m one
  printf 'changed\n' >>"$dirty_repo/f"
  dirty_repo="$(cd "$dirty_repo" && pwd)"
  clean_repo="$(cd "$clean_repo" && pwd)"
  run _install --override "pg-dirty=$dirty_repo" --override "pg-clean=$clean_repo"
  [ "$status" -eq 0 ]
  local src="$SHARED/gen-1/source.json"
  [ "$(jq -r '.overrides[0].name' "$src")" = pg-dirty ]
  [ "$(jq -r '.overrides[0].path' "$src")" = "$dirty_repo" ]
  [ "$(jq -r '.overrides[0].head' "$src")" = "$(_head_of "$dirty_repo")" ]
  [ "$(jq -r '.overrides[0].dirty' "$src")" = true ]
  [ "$(jq -r '.overrides[1].name' "$src")" = pg-clean ]
  [ "$(jq -r '.overrides[1].head' "$src")" = "$(_head_of "$clean_repo")" ]
  [ "$(jq -r '.overrides[1].dirty' "$src")" = false ]
}

@test "the private reinstall line renders --override-input pins and repeats the --override pins" {
  _mk_bundle
  _add_worktree a
  local ov="$GFH_ROOT/ov-one"
  gfh_init_repo "$ov" ov
  ov="$(cd "$ov" && pwd)"
  run bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private --override 'pg-x=$ov'"
  [ "$status" -eq 0 ]
  [ "$(cat "$WTGIT/pg-hooks/reinstall")" = "(cd $WT && nix run --override-input pg-x git+file://$ov .#install-pre-commit-hooks -- --private --override pg-x=$ov)" ]
}

# --- stubs -------------------------------------------------------------------

@test "stubs are rendered from the template with the stage substituted and are executable" {
  _mk_bundle '{"pre-commit":[{"id":"a","reason":"a"}],"pre-push":[{"id":"b","reason":"b"}],"commit-msg":[{"id":"c","reason":"c"}]}'
  run _install
  [ "$status" -eq 0 ]
  local s
  for s in pre-commit pre-push commit-msg; do
    [ -x "$COMMON/hooks/$s" ]
    [ "$(sed -n 2p "$COMMON/hooks/$s")" = '# managed-by: pg-hooks' ]
    grep -qx "stage='$s'" "$COMMON/hooks/$s"
    run ! grep -q '@STAGE@' "$COMMON/hooks/$s"
    # Identical to the template apart from the stage.
    [ "$(sed "s/@STAGE@/$s/g" "$PGH_T_STUB_TEMPLATE")" = "$(cat "$COMMON/hooks/$s")" ]
  done
  run ! grep -q '/nix/store' "$COMMON/hooks/pre-commit"
}

@test "non-hook stages (manual) get no stub" {
  _mk_bundle '{"pre-commit":[{"id":"a","reason":"a"}],"manual":[{"id":"m","reason":"m"}]}'
  run _install
  [ "$status" -eq 0 ]
  [ -e "$COMMON/hooks/pre-commit" ]
  [ ! -e "$COMMON/hooks/manual" ]
}

@test "own and legacy-marked stubs are replaced; stale own stubs removed" {
  _mk_bundle
  mkdir -p "$COMMON/hooks"
  # A prek-generated shim (legacy marker) at the needed stage.
  printf '#!/usr/bin/env bash\n# File generated by prek: https://github.com/j178/prek\nexit 0\n' >"$COMMON/hooks/pre-commit"
  chmod +x "$COMMON/hooks/pre-commit"
  # An own stub for a stage that no longer has hooks.
  printf '#!/bin/sh\n# managed-by: pg-hooks\nstage=pre-push\n' >"$COMMON/hooks/pre-push"
  chmod +x "$COMMON/hooks/pre-push"
  run --separate-stderr _install
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(sed -n 2p "$COMMON/hooks/pre-commit")" = '# managed-by: pg-hooks' ]
  run ! grep -q 'File generated by prek' "$COMMON/hooks/pre-commit"
  [ ! -e "$COMMON/hooks/pre-push" ]
}

@test "reinstalling is idempotent: the same stub text, still executable" {
  _mk_bundle
  run _install
  local first
  first="$(cat "$COMMON/hooks/pre-commit")"
  run _install
  [ "$status" -eq 0 ]
  [ "$(cat "$COMMON/hooks/pre-commit")" = "$first" ]
  [ -x "$COMMON/hooks/pre-commit" ]
}

@test "foreign file at a needed stage refuses with its path and changes nothing" {
  _mk_bundle
  mkdir -p "$COMMON/hooks"
  printf '#!/bin/sh\necho mine\n' >"$COMMON/hooks/pre-commit"
  chmod +x "$COMMON/hooks/pre-commit"
  run --separate-stderr _install
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [ "$stderr" = "pg-hooks: install refused: $COMMON/hooks/pre-commit is not managed by pg-hooks (first line: #!/bin/sh). Move it aside or chain it yourself, then rerun: (cd $CANON && nix run .#install-pre-commit-hooks)" ]
  [ "$(cat "$COMMON/hooks/pre-commit")" = "$(printf '#!/bin/sh\necho mine')" ]
  # Refusing before any change: no pointer, no generation.
  [ ! -e "$SHARED/current" ]
  [ ! -e "$SHARED/gen-1" ]
}

@test "foreign file at another stage (bd post-merge) is untouched" {
  _mk_bundle
  mkdir -p "$COMMON/hooks"
  printf '#!/bin/sh\n# bd (beads) post-merge hook\n' >"$COMMON/hooks/post-merge"
  chmod +x "$COMMON/hooks/post-merge"
  local before
  before="$(cat "$COMMON/hooks/post-merge")"
  run _install
  [ "$status" -eq 0 ]
  [ "$(cat "$COMMON/hooks/post-merge")" = "$before" ]
  [ -x "$COMMON/hooks/pre-commit" ]
}

@test "repo with zero hooks writes a bundle and no stubs" {
  _mk_bundle '{"pre-commit":[]}'
  run _install
  [ "$status" -eq 0 ]
  [ "$(_cur)" = gen-1 ]
  [ ! -e "$COMMON/hooks/pre-commit" ]
  rm -rf "$SHARED"
  _mk_bundle '{}'
  run _install
  [ "$status" -eq 0 ]
  [ "$(_cur)" = gen-1 ]
}

@test "never writes core.hooksPath (the git config is byte-identical)" {
  _mk_bundle
  cp "$COMMON/config" "$GFH_ROOT/config.before"
  run _install
  [ "$status" -eq 0 ]
  cmp "$GFH_ROOT/config.before" "$COMMON/config"
  run command git -C "$GFH_REPO" config --get core.hooksPath
  [ "$status" -eq 1 ]
  _add_worktree a
  run bash -c "cd '$WT' && '$PGH_T_SUT' --bundle '$B' --private"
  [ "$status" -eq 0 ]
  cmp "$GFH_ROOT/config.before" "$COMMON/config"
  [ ! -e "$WTGIT/config.worktree" ]
}

# --- paths and the stub end to end --------------------------------------------

@test "installer works when the clone path has a space" {
  _mk_bundle
  local spaced="$GFH_WORK/clone with space/repo"
  gfh_init_repo "$spaced" spaced
  command git -C "$spaced" config --unset core.hooksPath
  printf 'x\n' >"$spaced/file.txt"
  command git -C "$spaced" add file.txt
  command git -C "$spaced" commit -q -m init
  spaced="$(cd "$spaced" && pwd -P)"
  run --separate-stderr bash -c "cd '$spaced' && '$PGH_T_SUT' --bundle '$B'"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  [ "$(cat "$spaced/.git/pg-hooks/current")" = gen-1 ]
  # The reinstall line is shell-quoted so it can be pasted.
  [ "$(cat "$spaced/.git/pg-hooks/reinstall")" = "(cd $(printf '%q' "$spaced") && nix run .#install-pre-commit-hooks)" ]
  local quoted
  quoted="$(sed 's/^(cd //; s/ && nix.*$//' "$spaced/.git/pg-hooks/reinstall")"
  eval "set -- $quoted"
  [ "$1" = "$spaced" ]
  # And the installed stub reaches the bundle's runner through that pointer.
  run bash -c "cd '$spaced' && .git/hooks/pre-commit"
  [ "$status" -eq 0 ]
  [ "$output" = "ran pre-commit" ]
}

@test "the installed stub runs the bundle from the generation the pointer names" {
  _mk_bundle
  run _install
  [ "$status" -eq 0 ]
  run bash -c "cd '$GFH_REPO' && '$COMMON/hooks/pre-commit'"
  [ "$status" -eq 0 ]
  [ "$output" = "ran pre-commit" ]
}

@test "swap race: readers never miss during 500 swaps" {
  if [ "$(uname)" != Darwin ]; then
    skip "the APFS rename-vs-symlink race is measured on darwin"
  fi
  local d="$GFH_ROOT/race" i misses pid1 pid2
  mkdir -p "$d"
  pgh_write_pointer "$d" gen-1
  : >"$d/misses"
  _reader() {
    while [ ! -e "$d/stop" ]; do
      pgh_read_pointer "$d" >/dev/null || echo miss >>"$d/misses"
    done
  }
  _reader &
  pid1=$!
  _reader &
  pid2=$!
  for i in $(seq 1 500); do
    pgh_write_pointer "$d" "gen-$((i % 2 + 1))"
  done
  : >"$d/stop"
  wait "$pid1"
  wait "$pid2"
  misses="$(wc -l <"$d/misses" | tr -d ' ')"
  [ "$misses" -eq 0 ]
}
