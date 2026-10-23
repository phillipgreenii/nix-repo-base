#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

# pre-land-eval contract tests (beads pg2-d2yka and pg2-8uc2d; design pg2-3i12x
# section 9).
#
# pre-land-eval is a thin caller of `pn workspace flake-check --no-build`: the
# drvPath evaluation, the sibling overrides and the experiment log live in pn and
# are pinned by the Go tests there (including a real-nix test of this command line
# against a real pn). What this file pins is the caller's own contract: the
# per-clone opt-in key, the exact pn invocation, the one-word verdict with its
# 0/10/2/127 exit codes, and that it carries no evaluation logic of its own.
#
# Every test puts a `pn` SHIM first on PATH. The shim records its argv and working
# directory and replays stdout, stderr and an exit code from files in $SHIM_DIR.
# The repo under test is a REAL hermetic git fixture (git-fixture-harness), so
# the enablement key and the linked-worktree behaviour run against real git.
# PATH holds only the shim and symlinks to the few tools the script needs.

# shellcheck disable=SC2034  # PLE_* are read by the tests below
setup() {
  # The nix check injects SCRIPT_UNDER_TEST as an exported variable; gfh_setup
  # scrubs every exported variable not on its allowlist, so capture it first.
  PLE_SAVED_SUT="${SCRIPT_UNDER_TEST:-}"
  # shellcheck disable=SC1091  # runtime-resolved path
  source "${BATS_SUPPORT_PATH:-$BATS_TEST_DIRNAME/../../../../lib/scripts}/git-fixture-harness.bash"
  gfh_setup test-pre-land-eval

  REPO="$GFH_REPO"
  SHIM_DIR="$GFH_ROOT/shim"
  SHIM_BIN="$GFH_ROOT/shim-bin"
  MIN_BIN="$GFH_ROOT/min-bin"
  mkdir -p "$SHIM_DIR" "$SHIM_BIN" "$MIN_BIN"

  # The only real tools the script (and the shell wrapper around it) needs.
  local t p
  for t in bash sh git grep sed head tail wc tr mktemp rm cat dirname basename pwd env mkdir; do
    p="$(command -v "$t" 2>/dev/null || true)"
    [[ -n $p && -x $p ]] && ln -s "$p" "$MIN_BIN/$t"
  done

  # pn shim: record, then replay stdout, stderr and the exit code.
  cat >"$SHIM_BIN/pn" <<SHIM
#!/bin/sh
for a in "\$@"; do printf '%s\n' "\$a"; done >>"$SHIM_DIR/argv"
printf -- '--\n' >>"$SHIM_DIR/argv"
pwd -P >>"$SHIM_DIR/cwd"
env | grep -E '^GIT_(DIR|WORK_TREE|COMMON_DIR|INDEX_FILE)=' >>"$SHIM_DIR/git-env" || true
[ -f "$SHIM_DIR/pn.out" ] && cat "$SHIM_DIR/pn.out"
[ -f "$SHIM_DIR/pn.err" ] && cat "$SHIM_DIR/pn.err" >&2
if [ -f "$SHIM_DIR/pn.rc" ]; then exit "\$(cat "$SHIM_DIR/pn.rc")"; fi
exit 0
SHIM
  chmod +x "$SHIM_BIN/pn"

  if [[ -n $PLE_SAVED_SUT ]]; then
    SUT="$PLE_SAVED_SUT"
    SRC="${BATS_TEST_DIRNAME}/../pre-land-eval.sh"
  else
    # Raw source: the script has no library, so the builder's preamble is all it
    # is missing.
    SRC="${BATS_TEST_DIRNAME}/../pre-land-eval.sh"
    SUT="$GFH_ROOT/pre-land-eval"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "$SRC"
    } >"$SUT"
    chmod +x "$SUT"
  fi
}

teardown() {
  gfh_teardown
}

# sut <args>: run the tool with a PATH of only the shim and the few real tools it
# needs.
sut() {
  env PATH="$SHIM_BIN:$MIN_BIN" "$SUT" "$@"
}

# _enable [<repo>]: set the enablement key in the fixture repo.
_enable() {
  command git -C "${1:-$REPO}" config pgii-integrate-branch.preLandEval true
}

# _pn_rc <rc>: make the pn shim exit with <rc>.
_pn_rc() {
  printf '%s\n' "$1" >"$SHIM_DIR/pn.rc"
}

# ---------------------------------------------------------------------------
# Enablement

@test "an absent key prints not-enabled, exits 0 and never runs pn" {
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "not-enabled" ]
  [[ $stderr == *"pre-land eval: not enabled for $REPO"* ]]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "the key set to false is not-enabled" {
  command git -C "$REPO" config pgii-integrate-branch.preLandEval false
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "not-enabled" ]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "any value other than exactly true is not-enabled" {
  local v
  for v in yes 1 TRUE True on ""; do
    command git -C "$REPO" config pgii-integrate-branch.preLandEval "$v"
    run --separate-stderr sut "$REPO" HEAD
    [ "$status" -eq 0 ]
    [ "$output" = "not-enabled" ]
  done
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "not-enabled wins over a ref that does not resolve" {
  run --separate-stderr sut "$REPO" no-such-ref
  [ "$status" -eq 0 ]
  [ "$output" = "not-enabled" ]
}

@test "a directory that is not a git repository is not-enabled, not an error" {
  mkdir -p "$GFH_WORK/plain"
  run --separate-stderr sut "$GFH_WORK/plain" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "not-enabled" ]
}

@test "not-enabled does not need pn to be installed" {
  rm "$SHIM_BIN/pn"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "not-enabled" ]
}

# ---------------------------------------------------------------------------
# Usage

@test "no arguments is a usage error" {
  run --separate-stderr sut
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"expected <repo-path> <ref>"* ]]
}

@test "one argument is a usage error" {
  run --separate-stderr sut "$REPO"
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "three arguments is a usage error" {
  run --separate-stderr sut "$REPO" HEAD extra
  [ "$status" -eq 2 ]
  [ -z "$output" ]
}

@test "an unknown option is a usage error" {
  run --separate-stderr sut --bogus "$REPO" HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"unknown option: --bogus"* ]]
}

@test "--no-overrides is gone: the overrides are pn's, not the caller's" {
  _enable
  run --separate-stderr sut --no-overrides "$REPO" HEAD
  [ "$status" -eq 2 ]
  [[ $stderr == *"unknown option: --no-overrides"* ]]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "a repo path that is not a directory is a usage error" {
  run --separate-stderr sut "$GFH_WORK/nowhere" HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"not a directory"* ]]
}

@test "--help is answered locally with exit 0, names the pn command, and runs nothing" {
  run --separate-stderr sut --help
  [ "$status" -eq 0 ]
  [[ $output == "pre-land-eval:"* ]]
  [[ $output == *"pn workspace flake-check --no-build --caller ff-1c"* ]]
  [ ! -e "$SHIM_DIR/argv" ]
}

# ---------------------------------------------------------------------------
# The pn invocation

@test "it runs pn workspace flake-check --no-build as the ff-1c caller, with the ref and the absolute repo path" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  local want
  want="$(printf '%s\n' workspace flake-check --no-build --caller ff-1c --rev HEAD "$(cd "$REPO" && pwd -P)" --)"
  [ "$(cat "$SHIM_DIR/argv")" = "$want" ]
}

@test "pn runs from inside the repo, so it walks up to the workspace root" {
  _enable
  cd "$GFH_WORK"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$(cat "$SHIM_DIR/cwd")" = "$(cd "$REPO" && pwd -P)" ]
}

@test "--no-ifd is forwarded to pn, before or after the positional arguments" {
  _enable
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 0 ]
  grep -qx -- '--no-ifd' "$SHIM_DIR/argv"
  rm -f "$SHIM_DIR/argv"
  run --separate-stderr sut "$REPO" HEAD --no-ifd
  [ "$status" -eq 0 ]
  grep -qx -- '--no-ifd' "$SHIM_DIR/argv"
}

@test "without --no-ifd, pn is not told about IFD" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  run ! grep -q -- 'ifd' "$SHIM_DIR/argv"
}

@test "the ref is passed to pn verbatim: pn resolves it, not this tool" {
  _enable
  run --separate-stderr sut "$REPO" "refs/heads/main~0"
  [ "$status" -eq 0 ]
  grep -qx -- 'refs/heads/main~0' "$SHIM_DIR/argv"
}

@test "a linked worktree is passed to pn by its own path and reads the shared enablement key" {
  _enable
  command git -C "$REPO" worktree add -q -b wtbranch "$GFH_WORK/wt"
  run --separate-stderr sut "$GFH_WORK/wt" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -qx -- "$(cd "$GFH_WORK/wt" && pwd -P)" "$SHIM_DIR/argv"
  [ "$(cat "$SHIM_DIR/cwd")" = "$(cd "$GFH_WORK/wt" && pwd -P)" ]
}

@test "a leaked GIT_DIR does not redirect the tool or reach pn" {
  _enable
  GIT_DIR="$GFH_WORK/not-a-repo" run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ ! -s "$SHIM_DIR/git-env" ]
}

@test "a repo path with a space and a hash reaches pn as one argument" {
  mkdir -p "$GFH_WORK/odd dir#1"
  gfh_init_repo "$GFH_WORK/odd dir#1/r" test-pre-land-eval
  printf 'x\n' >"$GFH_WORK/odd dir#1/r/f"
  command git -C "$GFH_WORK/odd dir#1/r" add f
  command git -C "$GFH_WORK/odd dir#1/r" commit -q -m x
  _enable "$GFH_WORK/odd dir#1/r"
  run --separate-stderr sut "$GFH_WORK/odd dir#1/r" HEAD
  [ "$status" -eq 0 ]
  grep -qxF -- "$(cd "$GFH_WORK/odd dir#1/r" && pwd -P)" "$SHIM_DIR/argv"
}

# ---------------------------------------------------------------------------
# The verdict

@test "pn exit 0 prints exactly ok, and pn's own stdout goes to stderr" {
  _enable
  printf 'pn says hello\n' >"$SHIM_DIR/pn.out"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [[ $stderr == *"pn says hello"* ]]
}

@test "pn exit 1 (a failed check) is eval-failed, exit 10, with pn's output on stderr" {
  _enable
  _pn_rc 1
  printf '  checks.aarch64-darwin: FAILED\n' >"$SHIM_DIR/pn.out"
  printf 'error: The option `x.dockerState'"'"' does not exist.\n' >"$SHIM_DIR/pn.err"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
  [[ $stderr == *"checks.aarch64-darwin: FAILED"* ]]
  [[ $stderr == *"dockerState"* ]]
}

@test "pn exit 2 (unknown repo, unresolvable ref) is a usage error, exit 2, with no verdict word" {
  _enable
  _pn_rc 2
  printf 'ref does not resolve to a commit in /x: nope\n' >"$SHIM_DIR/pn.err"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"ref does not resolve"* ]]
}

@test "any other pn exit status means the check could not run: exit 2, no verdict word" {
  _enable
  local rc
  for rc in 3 127 130 143; do
    _pn_rc "$rc"
    run --separate-stderr sut "$REPO" HEAD
    [ "$status" -eq 2 ]
    [ -z "$output" ]
    [[ $stderr == *"exited $rc"* ]]
  done
}

@test "there is no retry: pn runs exactly once, even on failure" {
  _enable
  _pn_rc 1
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$(grep -c '^workspace$' "$SHIM_DIR/argv")" -eq 1 ]
}

@test "there is no inconclusive outcome: no verdict word but ok, eval-failed or not-enabled is ever printed" {
  _enable
  local rc
  for rc in 0 1 2 3 130; do
    _pn_rc "$rc"
    run --separate-stderr sut "$REPO" HEAD
    case "$output" in ok | eval-failed | "") ;; *) false ;; esac
  done
}

@test "pn not installed is exit 127 with the apply notice, and no verdict word" {
  _enable
  rm "$SHIM_BIN/pn"
  run -127 --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 127 ]
  [ -z "$output" ]
  [[ $stderr == *"pn not installed on this machine; ask the operator to run pn workspace apply"* ]]
}

# ---------------------------------------------------------------------------
# No evaluation logic of its own (bead pg2-8uc2d: ONE override derivation, in pn)

@test "the script runs no nix and derives no override or drvPath of its own" {
  # Comments explain what pn does; only executable lines count.
  local code
  code="$(grep -v '^[[:space:]]*#' "$SRC" | sed -e '/^show_help()/,/^HELP$/d')"
  run ! grep -E 'nix|override|drvPath|--apply|uname|pg-nix-log-wrapped|mapAttrs' <<<"$code"
}
