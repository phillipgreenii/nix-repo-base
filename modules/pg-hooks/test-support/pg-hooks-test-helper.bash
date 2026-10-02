# shellcheck shell=bash
# shellcheck disable=SC2034  # PGH_T_* values are read by the suites that source this helper

# Shared bats helper for the pg-hooks library and runner suites.
#
# It layers pg-hooks fixtures on top of the shared git fixture harness
# (lib/scripts/git-fixture-harness.bash): a fake bundle whose bin/prek logs what
# it was given, rendering of the stub template, and a PATH of symlinks for the
# "minimal environment" tests. The caller sources the harness first, captures
# the nix-injected variables into PGH_T_SAVED_* BEFORE gfh_setup (which scrubs
# every exported variable not on its allowlist), then calls pgh_t_setup.
#
# Plain (unexported) shell variables survive gfh_reset_env, which only touches
# exported ones, so every PGH_T_* value below stays readable after the reset.

# pgh_t_setup <suite>
#   PGH_T_SAVED_TS     nix: store dir with the shared test support, else empty
#   PGH_T_SAVED_LIB    nix: path to the composed library, else empty
#   PGH_T_SAVED_SUT    nix: the assembled runner, else empty
#   PGH_T_SAVED_SANDBOX  non-empty inside a nix build
#   PGH_T_DEFAULT_LIB  local fallback for the library
#   PGH_T_DEFAULT_RUN  local fallback for pg-hooks-run.sh ("" for lib-only suites)
pgh_t_setup() {
  local suite=$1
  gfh_setup "$suite"
  # The harness disables hooks with a local core.hooksPath=/dev/null. UNSET it:
  # git then runs <common-dir>/hooks, which is what the stubs are installed in.
  command git -C "$GFH_REPO" config --unset core.hooksPath

  PGH_T_COMMON="$GFH_REPO/.git"
  PGH_T_STUB_TEMPLATE="${PGH_T_SAVED_TS:-${BATS_TEST_DIRNAME}/../..}/stub.sh.in"
  PGH_T_LIB="${PGH_T_SAVED_LIB:-$PGH_T_DEFAULT_LIB}"
  PGH_T_LIB="${PGH_T_LIB%%:*}"
  PGH_T_REAL_GIT="$(command -v git)"
  PGH_T_CTL="$GFH_ROOT/prek-ctl"
  PGH_T_LOG="$GFH_ROOT/prek.log"
  mkdir -p "$PGH_T_CTL"

  PGH_T_SUT=""
  if [[ -n ${PGH_T_SAVED_SUT:-} ]]; then
    PGH_T_SUT="$PGH_T_SAVED_SUT"
  elif [[ -n ${PGH_T_DEFAULT_RUN:-} ]]; then
    # Running the raw .sh would fail: the builder prepends the library at build
    # time. Assemble the same composition (library, then the command's .sh).
    PGH_T_SUT="$GFH_ROOT/pg-hooks-run-wrapper"
    cat >"$PGH_T_SUT" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$PGH_T_LIB"
source "$PGH_T_DEFAULT_RUN"
WRAPPER
    chmod +x "$PGH_T_SUT"
  fi
}

# pgh_t_write_fake_prek <path>: a POSIX-sh stand-in using only builtins, so it
# also runs under `env -i` with a PATH that holds nothing but git and sh.
# It records argv, pwd, a few env vars and (when asked) stdin into $PGH_T_LOG.
pgh_t_write_fake_prek() {
  local path=$1
  cat >"$path" <<EOF
#!/bin/sh
log='$PGH_T_LOG'
ctl='$PGH_T_CTL'
printf 'x\\n' >>'$GFH_ROOT/prek-calls'
: >"\$log"
printf 'argc=%s\n' "\$#" >>"\$log"
for a in "\$@"; do printf 'arg=%s\n' "\$a" >>"\$log"; done
printf 'pwd=%s\n' "\$(pwd)" >>"\$log"
printf 'home=%s\n' "\${HOME-<unset>}" >>"\$log"
printf 'git_dir=%s\n' "\${GIT_DIR-<unset>}" >>"\$log"
printf 'git_index_file=%s\n' "\${GIT_INDEX_FILE-<unset>}" >>"\$log"
if [ -e "\$ctl/stdin" ]; then
  while IFS= read -r l || [ -n "\$l" ]; do printf 'stdin=%s\n' "\$l" >>"\$log"; done
fi
if [ -e "\$ctl/sleep" ]; then
  read -r s <"\$ctl/sleep"
  sleep "\$s"
fi
rc=0
if [ -f "\$ctl/exit" ]; then
  read -r rc <"\$ctl/exit"
fi
if [ "\$rc" != 0 ]; then
  echo 'FAKE-PREK: hook output' >&2
fi
exit "\$rc"
EOF
  chmod +x "$path"
}

# pgh_t_make_bundle [<pg-hooks dir> [<stamp>]]
# Builds a fake bundle and the pointer layout the installer would write:
#   <dir>/current            "gen-1"
#   <dir>/gen-1/bundle  ->   $GFH_ROOT/store/<n>  (a stand-in for the store path)
#   <dir>/gen-1/source.json
# <stamp> "current" (default) records the repo's present stamp. Sets PGH_T_BUNDLE.
pgh_t_make_bundle() {
  local dir=${1:-$PGH_T_COMMON/pg-hooks} stamp=${2:-current}
  local store
  store="$GFH_ROOT/store/$(basename "$(dirname "$dir")")-$RANDOM"
  mkdir -p "$store/bin" "$dir/gen-1"
  pgh_t_write_fake_prek "$store/bin/prek"
  if [[ -n $PGH_T_SUT ]]; then
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$PGH_T_SUT" >"$store/bin/pg-hooks-run"
  else
    printf '#!/bin/sh\nexit 0\n' >"$store/bin/pg-hooks-run"
  fi
  chmod +x "$store/bin/pg-hooks-run"
  printf '{}\n' >"$store/prek-config.json"
  printf '{"repo":"fixture","stampPaths":[],"stages":["pre-commit"]}\n' >"$store/meta.json"
  ln -s "$store" "$dir/gen-1/bundle"
  if [[ $stamp == current ]]; then
    stamp="$(cd "$GFH_REPO" && pgh_stamp flake.lock flake.nix)"
  fi
  jq -n --arg stamp "$stamp" --arg clone "$PGH_T_COMMON" \
    '{stamp: $stamp, overrides: [], clone_path: $clone, built_at: "2026-10-01T00:00:00Z"}' \
    >"$dir/gen-1/source.json"
  printf 'gen-1\n' >"$dir/current"
  PGH_T_BUNDLE="$store"
}

# pgh_t_install_stub <stage> [<hooks dir>]: render the stub template.
pgh_t_install_stub() {
  local stage=$1 hooks=${2:-$PGH_T_COMMON/hooks}
  mkdir -p "$hooks"
  sed "s/@STAGE@/$stage/g" "$PGH_T_STUB_TEMPLATE" >"$hooks/$stage"
  chmod +x "$hooks/$stage"
}

# pgh_t_track_flake_lock: commit a flake.lock/flake.nix pair so the stamp is known.
pgh_t_track_flake_lock() {
  printf '{"version":7}\n' >"$GFH_REPO/flake.lock"
  printf '{ }\n' >"$GFH_REPO/flake.nix"
  command git -C "$GFH_REPO" add flake.lock flake.nix
  command git -C "$GFH_REPO" commit -q -m "track flake files"
}

# pgh_t_commit [<message>]: change a tracked, non-stamp file and commit it, so
# the pre-commit hook fires. Extra args go to `git commit`.
pgh_t_commit() {
  local msg=${1:-change}
  shift || true
  printf '%s\n' "$msg $RANDOM" >>"$GFH_REPO/file.txt"
  command git -C "$GFH_REPO" add file.txt
  command git -C "$GFH_REPO" commit -q -m "$msg" "$@"
}

# pgh_t_limited_path: print a directory holding only symlinks to git and sh
# (plus what the raw-source runner wrapper needs when no assembled script exists).
pgh_t_limited_path() {
  local d="$GFH_ROOT/limited-bin" t
  mkdir -p "$d"
  ln -sf "$(command -v git)" "$d/git"
  ln -sf "$(command -v sh)" "$d/sh"
  if [[ -z ${PGH_T_SAVED_SUT:-} ]]; then
    for t in bash jq mkdir sleep dirname; do
      ln -sf "$(command -v "$t")" "$d/$t"
    done
  fi
  printf '%s\n' "$d"
}
