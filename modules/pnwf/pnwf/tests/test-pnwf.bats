#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Test suite for the `pnwf` CLI: resolve/repos/stage (the three read-only
# subcommands implemented in this task) + dispatch/help. Every test drives
# the ASSEMBLED artifact via SCRIPT_UNDER_TEST (bead pg2-28wwb convention):
# in the nix check, that is the real wrapped binary; for a local `bats
# tests/` run (no nix build), setup() below assembles an equivalent wrapper
# that sources pnwf-lib.bash then pnwf.sh in the same order the builder
# composes them, so this suite is genuinely RED before pnwf.sh exists and
# GREEN once it's implemented — not merely skipped locally.
#
# `pn` is mocked (never the real binary): the mock records whether
# PN_WORKSPACE_ROOT was set in ITS OWN environment (one line per invocation,
# to MOCK_PN_ENV_LOG) and answers `workspace info --json` by walking up from
# PN_WORKSPACE_ROOT (if set) or else $PWD looking for a `.mock-pn-info.json`
# marker — mirroring pn's own PN_WORKSPACE_ROOT-then-cwd-walk precedence
# closely enough to prove the H2/CRUX guard below either way: via the
# recorded env line, AND via which canned payload comes back.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../lib" && pwd)/pnwf-lib.bash"
  fi

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  if [[ -z ${SCRIPT_UNDER_TEST:-} ]]; then
    # Local dev: assemble a wrapper replicating the builder's composition
    # (library sourced before the command's .sh) — see bash-scripting
    # skill's "Library wrapper pattern".
    local resolved_lib
    if [[ -d ${LIB_PATH} ]]; then
      resolved_lib="${LIB_PATH}/pnwf-lib.bash"
    else
      resolved_lib="${LIB_PATH%%:*}"
    fi
    cat >"$TEST_DIR/pnwf-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${resolved_lib}"
source "${SCRIPTS_DIR}/pnwf.sh"
WRAPPER
    chmod +x "$TEST_DIR/pnwf-wrapper"
    SCRIPT_UNDER_TEST="$TEST_DIR/pnwf-wrapper"
  fi
  export SCRIPT_UNDER_TEST

  # Mocks live OUTSIDE any git working tree a test creates (pnwf itself
  # never `git clean`s, but this keeps the pattern consistent with the rest
  # of the module — see testing-advanced.md's mock-isolation gotcha).
  MOCK_BIN="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_BIN"
  PATH="$MOCK_BIN:$PATH"
  export PATH MOCK_BIN

  MOCK_PN_ENV_LOG="$TEST_DIR/pn-env.log"
  : >"$MOCK_PN_ENV_LOG"
  export MOCK_PN_ENV_LOG

  cat >"$MOCK_BIN/pn" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_PN_ENV_LOG:?MOCK_PN_ENV_LOG not set}"
if [[ -n "${PN_WORKSPACE_ROOT+x}" ]]; then
  echo "PN_WORKSPACE_ROOT=${PN_WORKSPACE_ROOT}" >>"$MOCK_PN_ENV_LOG"
else
  echo "PN_WORKSPACE_ROOT=<unset>" >>"$MOCK_PN_ENV_LOG"
fi

if [[ "${1:-}" == "workspace" && "${2:-}" == "info" ]]; then
  search_dir="${PN_WORKSPACE_ROOT:-$PWD}"
  while [[ -n "$search_dir" && "$search_dir" != "/" ]]; do
    if [[ -f "$search_dir/.mock-pn-info.json" ]]; then
      cat "$search_dir/.mock-pn-info.json"
      exit 0
    fi
    search_dir="$(dirname "$search_dir")"
  done
  echo "mock pn: no .mock-pn-info.json found (search root: ${PN_WORKSPACE_ROOT:-$PWD})" >&2
  exit 1
fi

echo "mock pn: unsupported invocation: $*" >&2
exit 1
MOCK
  chmod +x "$MOCK_BIN/pn"

  # Default integrate-branch-support mock (needed by `stage`, via
  # pnwf_resolve_primary_branch): called bare, emits JSON unconditionally.
  cat >"$MOCK_BIN/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_BIN/integrate-branch-support"

  CANONICAL_DIR="$TEST_DIR/canonical"
  mkdir -p "$CANONICAL_DIR"
  jq -n --arg root "$CANONICAL_DIR" '{
    wsid: "test-ws",
    root: $root,
    terminal: "repoA",
    workforests_dir: ".workforests",
    in_workforest: false,
    canonical_root: $root,
    repos: []
  }' >"$CANONICAL_DIR/.mock-pn-info.json"
  export CANONICAL_DIR

  BRANCH="feature-x"
  export BRANCH
  SET_DIR="$CANONICAL_DIR/.workforests/$BRANCH"
  mkdir -p "$SET_DIR"
  jq -n --arg root "$SET_DIR" --arg canonical "$CANONICAL_DIR" '{
    wsid: "test-ws",
    root: $root,
    terminal: "repoA",
    workforests_dir: ".workforests",
    in_workforest: true,
    canonical_root: $canonical,
    repos: []
  }' >"$SET_DIR/.mock-pn-info.json"
  export SET_DIR
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- fixture helpers (stage) ------------------------------------------------

_stage_write_lock() {
  local order_json
  order_json=$(printf '%s\n' "$@" | jq -R . | jq -s .)
  jq -n --argjson order "$order_json" '{order: $order, repos: {}, edges: []}' \
    >"$SET_DIR/pn-workspace.lock.json"
}

# Creates a real canonical git repo for $1 (one commit on main) plus a real
# `git worktree add` checkout of $BRANCH into the set dir — mirroring pn's
# own WorkforestAdd, so members share one object database the way a real
# workforest set does.
_stage_init_member() {
  local member="$1"
  local canon="$CANONICAL_DIR/$member"
  mkdir -p "$canon"
  command git -C "$canon" init -q -b main
  command git -C "$canon" config user.email "test@example.com"
  command git -C "$canon" config user.name "Test"
  echo one >"$canon/file.txt"
  command git -C "$canon" add file.txt
  command git -C "$canon" commit -q -m initial
  command git -C "$canon" worktree add -q "$SET_DIR/$member" -b "$BRANCH"
}

# --- resolve ----------------------------------------------------------------

@test "resolve on canned canonical info reports in_workforest=false and no set_dir" {
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" resolve
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.in_workforest')" = "false" ]
  [ "$(echo "$output" | jq -r '.canonical_root')" = "$CANONICAL_DIR" ]
  [ "$(echo "$output" | jq -r '.set_dir')" = "null" ]
  [ "$(echo "$output" | jq -r '.pn_workspace_root')" = "$CANONICAL_DIR" ]
}

@test "resolve on canned set info reports in_workforest=true and the correct pn_workspace_root" {
  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" resolve
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.in_workforest')" = "true" ]
  [ "$(echo "$output" | jq -r '.canonical_root')" = "$CANONICAL_DIR" ]
  [ "$(echo "$output" | jq -r '.set_dir')" = "$SET_DIR" ]
  [ "$(echo "$output" | jq -r '.pn_workspace_root')" = "$SET_DIR" ]
}

# CRUX (H2): a stale exported PN_WORKSPACE_ROOT pointing at canonical MUST
# NOT defeat resolve while cwd is actually inside the set. Verified two
# ways: (a) the JSON returned is still the SET's info, and (b) the mock's
# own recorded env shows PN_WORKSPACE_ROOT was unset when `pn` ran.
@test "CRUX: resolve returns SET info from cwd-in-set even with PN_WORKSPACE_ROOT exported to canonical" {
  cd "$SET_DIR"
  export PN_WORKSPACE_ROOT="$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" resolve
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.in_workforest')" = "true" ]
  [ "$(echo "$output" | jq -r '.set_dir')" = "$SET_DIR" ]

  run cat "$MOCK_PN_ENV_LOG"
  [ "$status" -eq 0 ]
  [[ "$output" == *"PN_WORKSPACE_ROOT=<unset>"* ]]
  [[ "$output" != *"PN_WORKSPACE_ROOT=$CANONICAL_DIR"* ]]
}

@test "resolve --set exits non-zero on a guard violation (asked in-set, info says not)" {
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" resolve --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"not in_workforest"* ]]
}

# --- MOCK-KEY-PARITY (M3) ----------------------------------------------------

@test "canned mock info json keys equal the real WorkspaceInfo json tags (guards mock drift)" {
  local repo_root info_go real_tags canonical_tags set_tags
  repo_root="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../../.." && pwd)"
  info_go="$repo_root/modules/pn/internal/workspace/info.go"
  if [[ ! -f "$info_go" ]]; then
    skip "info.go not present in this sandbox (nix check packages only pnwf's own src)"
  fi

  # Only the WorkspaceInfo struct block (not RepoInfo's nested tags below it).
  real_tags=$(awk '/type WorkspaceInfo struct/{f=1} f{print} f && /^}/{exit}' "$info_go" |
    grep -oE 'json:"[a-zA-Z_]+"' | sed -E 's/json:"(.*)"/\1/' | sort)
  [ -n "$real_tags" ]

  canonical_tags=$(jq -r 'keys[]' "$CANONICAL_DIR/.mock-pn-info.json" | sort)
  set_tags=$(jq -r 'keys[]' "$SET_DIR/.mock-pn-info.json" | sort)

  [ "$real_tags" = "$canonical_tags" ]
  [ "$real_tags" = "$set_tags" ]
}

# --- help / dispatch ---------------------------------------------------------

@test "pnwf --help exits 0 and prints usage" {
  run "$SCRIPT_UNDER_TEST" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: pnwf"* ]]
  [[ "$output" == *"resolve"* ]]
  [[ "$output" == *"repos"* ]]
  [[ "$output" == *"stage"* ]]
}

@test "unknown subcommand exits non-zero with a message" {
  run "$SCRIPT_UNDER_TEST" bogus-subcommand
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown subcommand"* ]]
}

@test "the five deferred subcommands exit non-zero as not yet implemented" {
  local sub
  for sub in fork-preflight land-plan cleanup status sync-fetch; do
    run "$SCRIPT_UNDER_TEST" "$sub"
    [ "$status" -ne 0 ]
    [[ "$output" == *"not yet implemented"* ]]
  done
}

# --- repos -------------------------------------------------------------------

@test "repos --set reads a fixture set lock in topo order" {
  _stage_write_lock repoA repoB repoC repoD repoE repoF
  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" repos --set
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 6 ]
  [ "${lines[0]}" = "repoA" ]
  [ "${lines[1]}" = "repoB" ]
  [ "${lines[2]}" = "repoC" ]
  [ "${lines[3]}" = "repoD" ]
  [ "${lines[4]}" = "repoE" ]
  [ "${lines[5]}" = "repoF" ]
}

@test "repos --set on a subset lock (2 of 6) prints only those two members" {
  _stage_write_lock repoC repoA
  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" repos --set
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 2 ]
  [ "${lines[0]}" = "repoC" ]
  [ "${lines[1]}" = "repoA" ]
}

@test "repos --set exits non-zero on a guard violation" {
  _stage_write_lock repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" repos --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"not in_workforest"* ]]
}

# --- stage -------------------------------------------------------------------
# Real git fixtures throughout (§3.2 derives the stage purely from git); `pn`
# is still mocked for the info --json lookup, `integrate-branch-support` for
# primary-branch resolution.

@test "stage --set: work (uncommitted changes in a present member worktree)" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo untracked >"$SET_DIR/repoA/extra.txt"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" stage --set
  [ "$status" -eq 0 ]
  [ "$output" = "work" ]
}

@test "stage --set: ready-to-land (clean, a member branch ahead of primary)" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" stage --set
  [ "$status" -eq 0 ]
  [ "$output" = "ready-to-land" ]
}

@test "stage --set: resuming-land (a member worktree absent, its branch un-landed)" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  rm -rf "$SET_DIR/repoA"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" stage --set
  [ "$status" -eq 0 ]
  [ "$output" = "resuming-land" ]
}

@test "stage --set: landed (every member branch is an ancestor of primary, or gone)" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  # Simulate a completed FF-4 land: merge repoA's branch into canonical main,
  # then remove its worktree + delete the branch (never `git branch -d` as
  # the landed-test itself — this merge is real, matching cleanup's own
  # merge-base ancestor rule).
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"
  command git -C "$CANONICAL_DIR/repoA" worktree remove --force "$SET_DIR/repoA"
  command git -C "$CANONICAL_DIR/repoA" branch -D "$BRANCH"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" stage --set
  [ "$status" -eq 0 ]
  [ "$output" = "landed" ]
}

@test "stage --set exits non-zero on a guard violation" {
  _stage_write_lock repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" stage --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"not in_workforest"* ]]
}
