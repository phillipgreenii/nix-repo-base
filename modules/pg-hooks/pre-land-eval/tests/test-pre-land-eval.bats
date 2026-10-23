#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

# pre-land-eval contract tests (bead pg2-d2yka; design pg2-3i12x section 9).
#
# A mkBashScript check sandbox has no nix daemon, so every test puts a `nix` SHIM
# first on PATH. The shim records its argv, its working directory and the
# environment it was run with, and replays stderr, stdout and an exit code per
# attribute set (checks, packages, devShells) from files in $SHIM_DIR. The repo
# under test is a REAL hermetic git fixture (git-fixture-harness), so the
# enablement key, ref resolution, the dirty-tree and the linked-worktree
# behaviour run against real git. PATH holds only the shims and symlinks to the
# few tools the script needs, so a pg-nix-log-wrapped on the host cannot leak in.

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
  for t in bash sh git grep sed head tail wc tr mktemp rm cat dirname basename pwd env mkdir uname; do
    p="$(command -v "$t" 2>/dev/null || true)"
    [[ -n $p && -x $p ]] && ln -s "$p" "$MIN_BIN/$t"
  done

  # nix shim: record, then replay by attribute set.
  cat >"$SHIM_BIN/nix" <<SHIM
#!/bin/sh
printf '%s\n' "\$*" >>"$SHIM_DIR/argv"
pwd >>"$SHIM_DIR/cwd"
attr=""
for a in "\$@"; do
  case \$a in *'#'*) attr=\$a ;; esac
done
set=\${attr#*#}
set=\${set%%.*}
[ -f "$SHIM_DIR/\$set.err" ] && cat "$SHIM_DIR/\$set.err" >&2
if [ -f "$SHIM_DIR/\$set.out" ]; then
  cat "$SHIM_DIR/\$set.out"
else
  printf '{"a":"/nix/store/aaaaaaaa-a.drv","b":"/nix/store/bbbbbbbb-b.drv"}\n'
fi
if [ -f "$SHIM_DIR/\$set.rc" ]; then exit "\$(cat "$SHIM_DIR/\$set.rc")"; fi
exit 0
SHIM
  chmod +x "$SHIM_BIN/nix"

  # uname shim: Darwin arm64 unless a test writes $SHIM_DIR/uname-s / uname-m.
  cat >"$SHIM_BIN/uname" <<SHIM
#!/bin/sh
s=Darwin; m=arm64
[ -f "$SHIM_DIR/uname-s" ] && s=\$(cat "$SHIM_DIR/uname-s")
[ -f "$SHIM_DIR/uname-m" ] && m=\$(cat "$SHIM_DIR/uname-m")
case "\$1" in
-s) echo "\$s" ;;
-m) echo "\$m" ;;
*) echo "\$s \$m" ;;
esac
SHIM
  chmod +x "$SHIM_BIN/uname"


  if [[ -n $PLE_SAVED_SUT ]]; then
    SUT="$PLE_SAVED_SUT"
  else
    # Raw source: the script has no library, so the builder's preamble is all it
    # is missing.
    SUT="$GFH_ROOT/pre-land-eval"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "${BATS_TEST_DIRNAME}/../pre-land-eval.sh"
    } >"$SUT"
    chmod +x "$SUT"
  fi
}

teardown() {
  gfh_teardown
}

# sut <args>: run the tool with a PATH of only the shims and the few real tools it
# needs, so a pg-nix-log-wrapped on the host never leaks in.
sut() {
  env PATH="$SHIM_BIN:$MIN_BIN" "$SUT" "$@"
}

# _enable [<repo>]: set the enablement key in the fixture repo.
_enable() {
  command git -C "${1:-$REPO}" config pgii-integrate-branch.preLandEval true
}

_head() {
  command git -C "$REPO" rev-parse HEAD
}

# _fail <set> <rc> <stderr text>: make the shim fail for one attribute set.
_fail() {
  printf '%s\n' "$3" >"$SHIM_DIR/$1.err"
  printf '%s\n' "$2" >"$SHIM_DIR/$1.rc"
}

# Captured stderr texts (nix 2.34 shapes).
ERR_NO_OPTION="error:
       … while evaluating the option \`system.build.toplevel':

       … while evaluating the module system

       error: The option \`phillipgreenii.observability.dockerState' does not exist. Definition values:
       - In \`/nix/store/aaaa-source/modules/zm/default.nix':"

ERR_IFD="error:
       … while evaluating the attribute 'test-claude-hook-router'

       error: cannot build '/nix/store/bbbb-claude-hook-router-marketplace-src.drv^out' during evaluation because the option 'allow-import-from-derivation' is disabled"

ERR_THROW="error:
       … while evaluating attribute 'bad'

       … while calling the 'throw' builtin

       error: boom"

ERR_ABSENT="error: flake 'git+file:///tmp/x?rev=abc' does not provide attribute 'packages.aarch64-darwin.devShells.aarch64-darwin', 'legacyPackages.aarch64-darwin.devShells.aarch64-darwin' or 'devShells.aarch64-darwin'"

# ---------------------------------------------------------------------------
# Enablement

@test "an absent key prints not-enabled, exits 0 and never runs nix" {
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

# ---------------------------------------------------------------------------
# Usage and ref resolution

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

@test "a repo path that is not a directory is a usage error" {
  run --separate-stderr sut "$GFH_WORK/nowhere" HEAD
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"not a directory"* ]]
}

@test "a ref that does not resolve is a usage error (enabled repo)" {
  _enable
  run --separate-stderr sut "$REPO" no-such-ref
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"ref does not resolve"* ]]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "--help is answered locally with exit 0" {
  run --separate-stderr sut --help
  [ "$status" -eq 0 ]
  [[ $output == "pre-land-eval:"* ]]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "a branch name, a full sha, a short sha and HEAD each evaluate the commit they name" {
  _enable
  local first second
  first="$(_head)"
  command git -C "$REPO" checkout -q -b feature
  printf 'two\n' >"$REPO/two.txt"
  command git -C "$REPO" add two.txt
  command git -C "$REPO" commit -q -m two
  second="$(_head)"
  command git -C "$REPO" checkout -q main

  run --separate-stderr sut "$REPO" feature
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q "?rev=$second#checks\." "$SHIM_DIR/argv"
  run ! grep -q "?rev=$first" "$SHIM_DIR/argv"

  rm -f "$SHIM_DIR/argv"
  run --separate-stderr sut "$REPO" "$first"
  [ "$output" = "ok" ]
  grep -q "?rev=$first#checks\." "$SHIM_DIR/argv"

  rm -f "$SHIM_DIR/argv"
  run --separate-stderr sut "$REPO" "${first:0:9}"
  [ "$output" = "ok" ]
  grep -q "?rev=$first#checks\." "$SHIM_DIR/argv"

  rm -f "$SHIM_DIR/argv"
  run --separate-stderr sut "$REPO" HEAD
  [ "$output" = "ok" ]
  grep -q "?rev=$first#checks\." "$SHIM_DIR/argv"
}

@test "an annotated tag resolves to its commit" {
  _enable
  local sha
  sha="$(_head)"
  command git -C "$REPO" tag -a v1 -m v1
  run --separate-stderr sut "$REPO" v1
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q "?rev=$sha#checks\." "$SHIM_DIR/argv"
}

@test "a dirty tree and untracked files are ignored: only the commit is evaluated" {
  _enable
  local sha
  sha="$(_head)"
  printf 'changed\n' >"$REPO/file.txt"
  printf 'new\n' >"$REPO/untracked.txt"
  local before
  before="$(command git -C "$REPO" status --porcelain)"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q "git+file://$(cd "$REPO" && pwd -P)?rev=$sha#" "$SHIM_DIR/argv"
  # The tool never changes the tree it reads.
  [ "$(command git -C "$REPO" status --porcelain)" = "$before" ]
  [ "$(cat "$REPO/file.txt")" = "changed" ]
}

@test "nix is never run from inside the repo" {
  _enable
  cd "$GFH_WORK"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  run ! grep -q "$(cd "$REPO" && pwd -P)" "$SHIM_DIR/cwd"
}

@test "a linked worktree evaluates its own path and reads the shared enablement key" {
  _enable
  command git -C "$REPO" worktree add -q -b wtbranch "$GFH_WORK/wt"
  printf 'wt\n' >"$GFH_WORK/wt/wt.txt"
  command git -C "$GFH_WORK/wt" add wt.txt
  command git -C "$GFH_WORK/wt" commit -q -m wt
  local sha
  sha="$(command git -C "$GFH_WORK/wt" rev-parse HEAD)"
  run --separate-stderr sut "$GFH_WORK/wt" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q "git+file://$(cd "$GFH_WORK/wt" && pwd -P)?rev=$sha#" "$SHIM_DIR/argv"
}

@test "a leaked GIT_DIR does not redirect the tool to another repository" {
  _enable
  local sha
  sha="$(_head)"
  GIT_DIR="$GFH_WORK/not-a-repo" run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q "?rev=$sha#" "$SHIM_DIR/argv"
}

@test "a repo path with a space and a hash is URL-encoded in the flake ref" {
  mkdir -p "$GFH_WORK/odd dir#1"
  gfh_init_repo "$GFH_WORK/odd dir#1/r" test-pre-land-eval
  printf 'x\n' >"$GFH_WORK/odd dir#1/r/f"
  command git -C "$GFH_WORK/odd dir#1/r" add f
  command git -C "$GFH_WORK/odd dir#1/r" commit -q -m x
  _enable "$GFH_WORK/odd dir#1/r"
  run --separate-stderr sut "$GFH_WORK/odd dir#1/r" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  grep -q 'odd%20dir%231/r?rev=' "$SHIM_DIR/argv"
}

# ---------------------------------------------------------------------------
# What is evaluated

@test "checks, packages and devShells are each evaluated once for the system from uname" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ "$(wc -l <"$SHIM_DIR/argv" | tr -d ' ')" = "3" ]
  grep -q '#checks\.aarch64-darwin ' "$SHIM_DIR/argv"
  grep -q '#packages\.aarch64-darwin ' "$SHIM_DIR/argv"
  grep -q '#devShells\.aarch64-darwin ' "$SHIM_DIR/argv"
}

@test "every nix call uses eval, --json, --no-write-lock-file and the drvPath apply expression" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  local line
  while IFS= read -r line; do
    [[ $line == *"eval "* ]]
    [[ $line == *"--json"* ]]
    [[ $line == *"--no-write-lock-file"* ]]
    [[ $line == *"--apply set: builtins.mapAttrs (n: v: v.drvPath or null) set"* ]]
    [[ $line != *"flake check"* ]]
  done <"$SHIM_DIR/argv"
}

@test "IFD is allowed by default: no allow-import-from-derivation option is passed" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  run ! grep -q 'allow-import-from-derivation' "$SHIM_DIR/argv"
}

@test "--no-ifd passes allow-import-from-derivation false to every call" {
  _enable
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ "$(grep -c -- '--option allow-import-from-derivation false' "$SHIM_DIR/argv")" = "3" ]
}

@test "--no-ifd is accepted after the positional arguments too" {
  _enable
  run --separate-stderr sut "$REPO" HEAD --no-ifd
  [ "$status" -eq 0 ]
  grep -q -- '--option allow-import-from-derivation false' "$SHIM_DIR/argv"
}

@test "x86_64 Linux derives x86_64-linux" {
  _enable
  printf 'Linux\n' >"$SHIM_DIR/uname-s"
  printf 'x86_64\n' >"$SHIM_DIR/uname-m"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  grep -q '#checks\.x86_64-linux ' "$SHIM_DIR/argv"
}

@test "aarch64 Linux derives aarch64-linux" {
  _enable
  printf 'Linux\n' >"$SHIM_DIR/uname-s"
  printf 'aarch64\n' >"$SHIM_DIR/uname-m"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  grep -q '#checks\.aarch64-linux ' "$SHIM_DIR/argv"
}

@test "a system it cannot name is eval-inconclusive and runs nothing" {
  _enable
  printf 'Plan9\n' >"$SHIM_DIR/uname-s"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 11 ]
  [ "$output" = "eval-inconclusive" ]
  [ ! -e "$SHIM_DIR/argv" ]
}

@test "the nix flake features are enabled on the command line" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  grep -q -- "--extra-experimental-features nix-command flakes" "$SHIM_DIR/argv"
}

@test "stderr carries a reproduce command and timings" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [[ $stderr == *"reproduce: nix eval"* ]]
  [[ $stderr == *"checks.aarch64-darwin: ok, 2 derivations evaluated ("*"s)"* ]]
  [[ $stderr == *"total "*"s evaluating aarch64-darwin at "* ]]
}

# ---------------------------------------------------------------------------
# Classification

@test "success prints exactly ok" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ "${#lines[@]}" -eq 1 ]
}

@test "an option that does not exist is eval-failed, exit 10, with nix's error on stderr" {
  _enable
  _fail checks 1 "$ERR_NO_OPTION"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
  [[ $stderr == *"phillipgreenii.observability.dockerState"* ]]
  [[ $stderr == *"eval failed in: checks"* ]]
}

@test "a throw in any set is eval-failed" {
  _enable
  local set
  for set in checks packages devShells; do
    rm -f "$SHIM_DIR"/*.err "$SHIM_DIR"/*.rc
    _fail "$set" 1 "$ERR_THROW"
    run --separate-stderr sut "$REPO" HEAD
    [ "$status" -eq 10 ]
    [ "$output" = "eval-failed" ]
  done
}

@test "all three sets are still evaluated after the first fails" {
  _enable
  _fail checks 1 "$ERR_THROW"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$(wc -l <"$SHIM_DIR/argv" | tr -d ' ')" = "3" ]
}

@test "the first lines of a long error are shown, not the whole of it" {
  _enable
  local big="error:"
  local i
  for i in $(seq 1 40); do big="$big
       line $i of a long trace"; done
  big="$big
       error: the final message"
  _fail checks 1 "$big"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [[ $stderr == *"line 1 of a long trace"* ]]
  [[ $stderr != *"line 40 of a long trace"* ]]
  # ... but the final message is repeated after the head.
  [[ $stderr == *"the final error message(s):"*"error: the final message"* ]]
}

@test "a non-zero nix exit with no recognisable error is eval-failed" {
  _enable
  _fail packages 137 "Killed: 9"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "a non-zero nix exit with empty stderr is eval-failed" {
  _enable
  printf '3\n' >"$SHIM_DIR/devShells.rc"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "an IFD error without --no-ifd is a real failure: IFD is allowed, so it should not have happened" {
  _enable
  _fail checks 1 "$ERR_IFD"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "--no-ifd classifies an IFD-only error as eval-inconclusive, exit 11" {
  _enable
  _fail checks 1 "$ERR_IFD"
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 11 ]
  [ "$output" = "eval-inconclusive" ]
  [[ $stderr == *"allow-import-from-derivation"* ]]
  [[ $stderr == *"inconclusive"* ]]
}

@test "--no-ifd with a genuine error alongside the IFD error in one stderr is eval-failed" {
  _enable
  _fail checks 1 "$ERR_IFD
$ERR_NO_OPTION"
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "--no-ifd with an IFD error in one set and a genuine error in another is eval-failed" {
  _enable
  _fail checks 1 "$ERR_IFD"
  _fail packages 1 "$ERR_THROW"
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "--no-ifd with IFD errors in several sets and nothing else is eval-inconclusive" {
  _enable
  _fail checks 1 "$ERR_IFD"
  _fail packages 1 "$ERR_IFD"
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 11 ]
  [ "$output" = "eval-inconclusive" ]
}

@test "--no-ifd does not turn a genuine error into inconclusive" {
  _enable
  _fail checks 1 "$ERR_NO_OPTION"
  run --separate-stderr sut --no-ifd "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "a set the flake does not provide is tolerated" {
  _enable
  _fail devShells 1 "$ERR_ABSENT"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [[ $stderr == *"devShells.aarch64-darwin: not provided by the flake, skipped"* ]]
}

@test "a flake that provides none of the three sets is ok, and says nothing was evaluated" {
  _enable
  _fail checks 1 "$ERR_ABSENT"
  _fail packages 1 "$ERR_ABSENT"
  _fail devShells 1 "$ERR_ABSENT"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [[ $stderr == *"nothing was evaluated"* ]]
}

@test "a missing set next to a genuine failure is still eval-failed" {
  _enable
  _fail devShells 1 "$ERR_ABSENT"
  _fail checks 1 "$ERR_THROW"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "a does-not-provide-attribute message from inside a larger error is NOT tolerated" {
  _enable
  _fail checks 1 "error:
       … while evaluating attribute 'x'

       error: attribute 'foo' missing
       error: flake 'git+file:///x' does not provide attribute 'bar'"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}

@test "exactly one word reaches stdout on every outcome" {
  _enable
  local out
  out="$(sut "$REPO" HEAD 2>/dev/null)"
  [ "$out" = "ok" ]
  _fail checks 1 "$ERR_THROW"
  out="$(sut "$REPO" HEAD 2>/dev/null || true)"
  [ "$out" = "eval-failed" ]
  rm -f "$SHIM_DIR"/*.err "$SHIM_DIR"/*.rc
  _fail checks 1 "$ERR_IFD"
  out="$(sut --no-ifd "$REPO" HEAD 2>/dev/null || true)"
  [ "$out" = "eval-inconclusive" ]
}

@test "any non-zero nix exit code maps to eval-failed, exit 10, never to the raw code" {
  _enable
  local rc
  for rc in 1 2 3 4 100 137; do
    rm -f "$SHIM_DIR"/*.err "$SHIM_DIR"/*.rc
    printf '%s\n' "$rc" >"$SHIM_DIR/checks.rc"
    run --separate-stderr sut "$REPO" HEAD
    [ "$status" -eq 10 ]
  done
}

# ---------------------------------------------------------------------------
# pg-nix-log-wrapped

@test "nix runs through pg-nix-log-wrapped when it is on PATH" {
  _enable
  cat >"$SHIM_BIN/pg-nix-log-wrapped" <<SHIM
#!/bin/sh
printf '%s\n' "\$*" >>"$SHIM_DIR/wrapper-argv"
exec "\$@"
SHIM
  chmod +x "$SHIM_BIN/pg-nix-log-wrapped"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ "$(wc -l <"$SHIM_DIR/wrapper-argv" | tr -d ' ')" = "3" ]
  [[ $(head -n 1 "$SHIM_DIR/wrapper-argv") == "nix "* ]]
  [[ $stderr != *"running nix directly"* ]]
  [[ $stderr == *"reproduce: pg-nix-log-wrapped nix eval"* ]]
}

@test "nix runs directly, with one stderr note, when pg-nix-log-wrapped is absent" {
  _enable
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 0 ]
  [ "$output" = "ok" ]
  [ "$(printf '%s\n' "$stderr" | grep -c 'running nix directly')" = "1" ]
  [ "$(wc -l <"$SHIM_DIR/argv" | tr -d ' ')" = "3" ]
}

@test "the wrapper's own failure exit is classified like nix's" {
  _enable
  cat >"$SHIM_BIN/pg-nix-log-wrapped" <<SHIM
#!/bin/sh
exec "\$@"
SHIM
  chmod +x "$SHIM_BIN/pg-nix-log-wrapped"
  _fail checks 1 "$ERR_THROW"
  run --separate-stderr sut "$REPO" HEAD
  [ "$status" -eq 10 ]
  [ "$output" = "eval-failed" ]
}
