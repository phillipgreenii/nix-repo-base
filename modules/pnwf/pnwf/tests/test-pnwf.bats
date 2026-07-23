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

  # Hermetic + fast git. A developer's global `core.fsmonitor=true` makes every
  # throwaway repo these tests `git init` spawn its own fsmonitor daemon that
  # blocks each working-tree op (commit/worktree/status) for 2-3s -- pushing the
  # full suite to ~20min locally (the nix-check sandbox is immune: clean HOME).
  # Inject core.fsmonitor/untrackedcache=false into EVERY git invocation in this
  # test process (GIT_CONFIG_COUNT works like a `-c` flag, so it wins over the
  # inherited global and is surgical -- it does not replace the rest of git
  # config), making the suite fast (~35s) AND deterministic regardless of whose
  # ~/.gitconfig runs it. Same class of fix as pg2-0sa8p (pn disabling fsmonitor
  # on its own git status probes). Applies to real-git tests; git-mock tests are
  # unaffected.
  export GIT_CONFIG_COUNT=2
  export GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false
  export GIT_CONFIG_KEY_1=core.untrackedcache GIT_CONFIG_VALUE_1=false

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

# `pn workspace workforest remove <branch>`: mirrors the real Go
# implementation closely enough for cleanup's tests -- requires
# PN_WORKSPACE_ROOT (the real WorkforestRemove resolves paths off the
# workspace ROOT, which must be canonical), and unconditionally deletes the
# now-emptied set dir.
if [[ "${1:-}" == "workspace" && "${2:-}" == "workforest" && "${3:-}" == "remove" ]]; then
  : "${PN_WORKSPACE_ROOT:?mock pn: workspace workforest remove requires PN_WORKSPACE_ROOT (must be pinned to canonical)}"
  branch="${4:-}"
  set_dir="$PN_WORKSPACE_ROOT/.workforests/$branch"
  if [[ ! -d "$set_dir" ]]; then
    echo "mock pn: workforest remove: set directory does not exist: $set_dir" >&2
    exit 1
  fi
  rm -rf "$set_dir"
  exit 0
fi

# `pn workspace update [--in-place]`: the relock step driven by `pnwf
# update-relock`. Prints MOCK_PN_UPDATE_OUTPUT (if set) as combined output and
# exits MOCK_PN_UPDATE_RC (default 0), so a test can drive a clean relock, a
# non-zero relock, and a "skipped a repo but still exited 0" relock without
# touching the real pn. (Its PN_WORKSPACE_ROOT env is already recorded above,
# proving update-relock cleared it via `env -u`.)
if [[ "${1:-}" == "workspace" && "${2:-}" == "update" ]]; then
  if [[ -n "${MOCK_PN_UPDATE_OUTPUT:-}" ]]; then
    printf '%s\n' "$MOCK_PN_UPDATE_OUTPUT"
  fi
  exit "${MOCK_PN_UPDATE_RC:-0}"
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

# --- fork-preflight fixture helpers -----------------------------------------

# A real canonical-only repo (no worktree, no workforest branch) -- for
# fork-preflight, which runs BEFORE any set exists.
_fp_init_canonical_repo() {
  local name="$1"
  local dir="$CANONICAL_DIR/$name"
  mkdir -p "$dir"
  command git -C "$dir" init -q -b main
  command git -C "$dir" config user.email "test@example.com"
  command git -C "$dir" config user.name "Test"
  echo one >"$dir/file.txt"
  command git -C "$dir" add file.txt
  command git -C "$dir" commit -q -m initial
}

# Overwrites CANONICAL_DIR's info fixture with a populated `.repos[]` for the
# given repo names (name/path only matter to fork-preflight; applied_ref and
# dirty are unused filler matching the real RepoInfo shape).
_fp_write_canonical_info() {
  local repos_json="[]" name
  for name in "$@"; do
    repos_json=$(printf '%s' "$repos_json" | jq --arg name "$name" --arg path "$CANONICAL_DIR/$name" \
      '. + [{name: $name, path: $path, applied_ref: "", dirty: false}]')
  done
  jq -n --arg root "$CANONICAL_DIR" --argjson repos "$repos_json" '{
    wsid: "test-ws",
    root: $root,
    terminal: "repoA",
    workforests_dir: ".workforests",
    in_workforest: false,
    canonical_root: $root,
    repos: $repos
  }' >"$CANONICAL_DIR/.mock-pn-info.json"
}

# --- fork-preflight ----------------------------------------------------------

@test "fork-preflight: cwd already inside a set -> stop" {
  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight "$BRANCH"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" == *"already inside a workforest set"* ]]
}

@test "fork-preflight: canonical repo off-primary -> stop" {
  _fp_init_canonical_repo repoA
  command git -C "$CANONICAL_DIR/repoA" checkout -q -b other
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" == *"repoA"* ]]
}

@test "fork-preflight: canonical repo dirty -> stop" {
  _fp_init_canonical_repo repoA
  echo dirty >"$CANONICAL_DIR/repoA/untracked.txt"
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" == *"repoA"* ]]
}

@test "fork-preflight: existing set dir -> resume" {
  _fp_init_canonical_repo repoA
  _fp_write_canonical_info repoA
  mkdir -p "$CANONICAL_DIR/.workforests/new-feature"
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "resume" ]
  [[ "$output" == *"set directory already exists"* ]]
}

@test "fork-preflight: existing branch in a member repo -> resume" {
  _fp_init_canonical_repo repoA
  command git -C "$CANONICAL_DIR/repoA" branch new-feature
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "resume" ]
  [[ "$output" == *"repoA"* ]]
}

@test "fork-preflight: clean canonical, no set, no branch -> proceed" {
  _fp_init_canonical_repo repoA
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "proceed" ]
}

@test "fork-preflight: --repos filters which repos are checked" {
  _fp_init_canonical_repo repoA
  _fp_init_canonical_repo repoB
  command git -C "$CANONICAL_DIR/repoB" checkout -q -b other
  _fp_write_canonical_info repoA repoB
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature --repos repoA
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "proceed" ]
}

@test "fork-preflight: without --repos, an off-primary sibling still stops" {
  _fp_init_canonical_repo repoA
  _fp_init_canonical_repo repoB
  command git -C "$CANONICAL_DIR/repoB" checkout -q -b other
  _fp_write_canonical_info repoA repoB
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" == *"repoB"* ]]
}

# --- land-plan ---------------------------------------------------------------

@test "land-plan: absent worktree is skipped even though its branch is not landed" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo two >"$SET_DIR/repoB/file.txt"
  command git -C "$SET_DIR/repoB" commit -q -am second
  rm -rf "$SET_DIR/repoB"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "land-plan: present not-landed member is included" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ "$output" = "repoA" ]
}

@test "land-plan: a present pull-request-strategy member (not landed) is included" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second

  cat >"$MOCK_BIN/integrate-branch-support" <<MOCK
#!/usr/bin/env bash
if [[ "\$PWD" == "$CANONICAL_DIR/repoA" ]]; then
  echo '{"primary_branch":"main","strategy":"pull-request"}'
else
  echo '{"primary_branch":"main","strategy":null}'
fi
MOCK
  chmod +x "$MOCK_BIN/integrate-branch-support"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ "$output" = "repoA" ]
}

@test "land-plan: landed member is excluded" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "land-plan: present worktree with an absent member branch (128) does not abort" {
  mkdir -p "$SET_DIR/repoC"
  mkdir -p "$CANONICAL_DIR/repoC"
  command git -C "$CANONICAL_DIR/repoC" init -q -b main
  command git -C "$CANONICAL_DIR/repoC" config user.email "test@example.com"
  command git -C "$CANONICAL_DIR/repoC" config user.name "Test"
  echo one >"$CANONICAL_DIR/repoC/file.txt"
  command git -C "$CANONICAL_DIR/repoC" add file.txt
  command git -C "$CANONICAL_DIR/repoC" commit -q -m initial

  _stage_write_lock repoC
  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ "$output" = "repoC" ]
}

@test "land-plan: subset lock excludes a physically-present member not in the lock" {
  _stage_init_member repoA
  _stage_init_member repoD
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoD/file.txt"
  command git -C "$SET_DIR/repoD" commit -q -am second

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" land-plan "$BRANCH"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

# --- status --------------------------------------------------------------

@test "status: absent worktree classifies as landed" {
  _stage_init_member repoA
  _stage_write_lock repoA
  rm -rf "$SET_DIR/repoA"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" status "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == "repoA"$'\t'"landed"$'\t'* ]]
}

@test "status: present clean zero-ahead member is not-started" {
  _stage_init_member repoA
  _stage_write_lock repoA

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" status "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == "repoA"$'\t'"not-started"$'\t'* ]]
}

@test "status: present clean ahead member is kept" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" status "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == "repoA"$'\t'"kept"$'\t'* ]]
  [[ "$output" == *"1 commit(s) ahead"* ]]
}

@test "status: present dirty member is blocked" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo untracked >"$SET_DIR/repoA/extra.txt"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" status "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == "repoA"$'\t'"blocked"$'\t'* ]]
}

@test "status: multi-member table lists each member's own state" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  echo two >"$SET_DIR/repoB/file.txt"
  command git -C "$SET_DIR/repoB" commit -q -am second

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" status "$BRANCH"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 2 ]
  [[ "${lines[0]}" == "repoA"$'\t'"not-started"* ]]
  [[ "${lines[1]}" == "repoB"$'\t'"kept"* ]]
}

# --- cleanup -----------------------------------------------------------------

@test "REVIEW-CRITICAL: cleanup processes landed+not-landed+absent-ref members without aborting, exit 0" {
  _stage_init_member repoA
  _stage_init_member repoB
  # repoC: a real canonical repo, but the workforest branch was never
  # created in it -- and it never got a worktree in the set either (mirrors
  # a member already fully cleaned up elsewhere, or never forked into).
  mkdir -p "$CANONICAL_DIR/repoC"
  command git -C "$CANONICAL_DIR/repoC" init -q -b main
  command git -C "$CANONICAL_DIR/repoC" config user.email "test@example.com"
  command git -C "$CANONICAL_DIR/repoC" config user.name "Test"
  echo one >"$CANONICAL_DIR/repoC/file.txt"
  command git -C "$CANONICAL_DIR/repoC" add file.txt
  command git -C "$CANONICAL_DIR/repoC" commit -q -m initial

  _stage_write_lock repoA repoB repoC

  # repoA: landed -- merge into canonical main; worktree + branch are still
  # present (exactly the state `pnwf cleanup` exists to finish tearing down).
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"

  # repoB: not landed (ahead of main, never merged).
  echo two >"$SET_DIR/repoB/file.txt"
  command git -C "$SET_DIR/repoB" commit -q -am second

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"

  # THE review-critical assertion: exit 0 despite B (exit 1) and C (exit 128).
  [ "$status" -eq 0 ]

  [[ "$output" == *"repoA"$'\t'"removed"* ]]
  [[ "$output" == *"repoB"$'\t'"kept"* ]]
  [[ "$output" == *"repoC"$'\t'"landed"* ]]
  [[ "$output" == *"(set)"$'\t'"kept"* ]]

  # B's report names BOTH force flags.
  b_line=$(printf '%s\n' "$output" | grep '^repoB')
  [[ "$b_line" == *"--force-unlanded-branch-removal"* ]]
  [[ "$b_line" == *"--force-dirty-worktree-removal"* ]]

  # A was actually removed on disk; B and C were left alone.
  [ ! -e "$SET_DIR/repoA" ]
  run bash -c "command git -C '$CANONICAL_DIR/repoA' rev-parse --verify --quiet refs/heads/$BRANCH"
  [ "$status" -ne 0 ]

  [ -e "$SET_DIR/repoB" ]
  run bash -c "command git -C '$CANONICAL_DIR/repoB' rev-parse --verify --quiet refs/heads/$BRANCH"
  [ "$status" -eq 0 ]

  # The set dir is left in place -- B is still kept.
  [ -e "$SET_DIR" ]
}

@test "cleanup: removes the set directory via 'pn workspace workforest remove' when nothing is kept" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"(set)"$'\t'"removed"* ]]
  [ ! -e "$SET_DIR" ]
}

@test "cleanup --force-dirty-worktree-removal removes a landed but dirty worktree" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"
  echo untracked >"$SET_DIR/repoA/extra.txt"

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"kept"* ]]
  [[ "$output" == *"--force-dirty-worktree-removal"* ]]
  [ -e "$SET_DIR/repoA" ]

  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH" --force-dirty-worktree-removal
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"removed"* ]]
  [ ! -e "$SET_DIR/repoA" ]
}

@test "cleanup --force-unlanded-branch-removal force-removes a not-landed member" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"kept"* ]]
  [ -e "$SET_DIR/repoA" ]

  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH" --force-unlanded-branch-removal
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"removed"* ]]
  [[ "$output" == *"forcibly removed"* ]]
  [ ! -e "$SET_DIR/repoA" ]
  run bash -c "command git -C '$CANONICAL_DIR/repoA' rev-parse --verify --quiet refs/heads/$BRANCH"
  [ "$status" -ne 0 ]
}

@test "cleanup: subset lock excludes a physically-present member from processing" {
  _stage_init_member repoA
  _stage_init_member repoX
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  echo two >"$SET_DIR/repoX/file.txt"
  command git -C "$SET_DIR/repoX" commit -q -am second

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"kept"* ]]
  [[ "$output" != *"repoX"* ]]
  [ -e "$SET_DIR/repoX" ]
  run bash -c "command git -C '$CANONICAL_DIR/repoX' rev-parse --verify --quiet refs/heads/$BRANCH"
  [ "$status" -eq 0 ]
  [ -e "$SET_DIR" ]
}

# --- sync-fetch ---------------------------------------------------------
# The one MUTATING WORK-recipe subcommand (task 5) -- unlike every probe
# above, `git` itself is MOCKED here rather than real (per its own test
# brief): the orchestration under test (stop on the FIRST failing member,
# do not continue, report repo+path with step-appropriate wording) doesn't
# need real fetch/rebase mechanics -- those (incl. the FETCH-step vs.
# REBASE-step return-code distinction pnwf_fetch_and_rebase itself signals)
# are proven with REAL git directly in test-pnwf-lib.bats. Member dirs are
# plain directories (no `.git` needed): sync-fetch's own git calls are
# fully mocked, and pnwf_resolve_primary_branch only needs `cd` into
# member_canonical before calling the (already-mocked)
# integrate-branch-support.
#
# The mock logs every invocation as "<dir> <subcommand>" to MOCK_GIT_LOG so
# tests can assert both WHICH members were touched and in what order --
# proving the loop stops at the first failure rather than merely reporting
# it while continuing underneath.

_sync_fetch_write_git_mock() {
  cat >"$MOCK_BIN/git" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
: "${MOCK_GIT_LOG:?MOCK_GIT_LOG not set}"

dir="$PWD"
if [[ "${1:-}" == "-C" ]]; then
  dir="$2"
  shift 2
fi

echo "$dir ${1:-}" >>"$MOCK_GIT_LOG"

case "${1:-}" in
fetch)
  if [[ -f "$dir/.mock-fetch-failure" ]]; then
    echo "mock git: fatal: could not read from remote repository" >&2
    exit 128
  fi
  exit 0
  ;;
rebase)
  if [[ -f "$dir/.mock-rebase-conflict" ]]; then
    echo "mock git: CONFLICT (content): Merge conflict in file.txt" >&2
    exit 1
  fi
  exit 0
  ;;
*)
  echo "mock git: unsupported invocation: $dir $*" >&2
  exit 1
  ;;
esac
MOCK
  chmod +x "$MOCK_BIN/git"
}

_sync_fetch_init_members() {
  local member
  for member in "$@"; do
    mkdir -p "$SET_DIR/$member" "$CANONICAL_DIR/$member"
  done
}

@test "sync-fetch --set: clean rebase across all members fetches+rebases each in topo order, exit 0" {
  _stage_write_lock repoA repoB repoC
  _sync_fetch_init_members repoA repoB repoC

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -eq 0 ]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 6 ]
  [ "${lines[0]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[1]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[2]}" = "$SET_DIR/repoB fetch" ]
  [ "${lines[3]}" = "$SET_DIR/repoB rebase" ]
  [ "${lines[4]}" = "$SET_DIR/repoC fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoC rebase" ]
}

@test "sync-fetch --set: conflicting rebase stops on the FIRST conflicting repo, reports repo+worktree with rebase-specific recovery, exits non-zero, and does not continue" {
  _stage_write_lock repoA repoB repoC
  _sync_fetch_init_members repoA repoB repoC
  touch "$SET_DIR/repoB/.mock-rebase-conflict"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"$SET_DIR/repoB"* ]]

  # Rebase-specific recovery wording: a conflict DID leave a rebase in
  # progress, so `git rebase --continue` is correct advice here (contrast
  # with the fetch-failure test below, where it would be wrong advice).
  [[ "$output" == *"rebase --continue"* ]]
  [[ "$output" != *"'git fetch origin' failed"* ]]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[0]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[1]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[2]}" = "$SET_DIR/repoB fetch" ]
  [ "${lines[3]}" = "$SET_DIR/repoB rebase" ]
  [[ "$output" != *"repoC"* ]]
}

@test "sync-fetch --set: a git fetch failure reports fetch-specific recovery (no rebase --continue), exits non-zero, and does not continue" {
  _stage_write_lock repoA repoB repoC
  _sync_fetch_init_members repoA repoB repoC
  touch "$SET_DIR/repoB/.mock-fetch-failure"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"$SET_DIR/repoB"* ]]

  # Fetch-specific recovery wording: no rebase was ever started here, so
  # `git rebase --continue` would be actively wrong advice -- it must NOT
  # appear.
  [[ "$output" == *"'git fetch origin' failed"* ]]
  [[ "$output" != *"rebase --continue"* ]]

  # The rebase step is never reached for the failing member, and repoC
  # (the later member) is never touched.
  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 3 ]
  [ "${lines[0]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[1]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[2]}" = "$SET_DIR/repoB fetch" ]
  [[ "$output" != *"repoC"* ]]
}

@test "sync-fetch --set: a member with an absent worktree is skipped, and sync-fetch continues to later members" {
  _stage_write_lock repoA repoB repoC
  # repoB: deliberately no $SET_DIR/repoB (and no $CANONICAL_DIR/repoB
  # either) -- mirrors an already-landed/cleaned-up member. sync-fetch must
  # skip it via pnwf_worktree_present before ever touching git or
  # integrate-branch-support for it, and continue on to repoC.
  _sync_fetch_init_members repoA repoC

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -eq 0 ]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [[ "$output" != *"repoB"* ]]
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[0]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[1]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[2]}" = "$SET_DIR/repoC fetch" ]
  [ "${lines[3]}" = "$SET_DIR/repoC rebase" ]
}

@test "sync-fetch --set: a re-run after a member is already up to date is a clean no-op for it" {
  _stage_write_lock repoA
  _sync_fetch_init_members repoA

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -eq 0 ]

  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -eq 0 ]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[0]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[1]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[2]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[3]}" = "$SET_DIR/repoA rebase" ]
}

@test "sync-fetch --set exits non-zero on a guard violation" {
  _stage_write_lock repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"not in_workforest"* ]]
}

# --- update-relock -------------------------------------------------------
# The second MUTATING WORK-recipe subcommand (the /pn-workspace-update recipe,
# parallel to sync-fetch for /pn-workspace-sync). REAL git throughout (its
# pre-flight -- upstream detection + tracked-change cleanliness -- is exactly
# the git state under test, so mocking git would defeat the point); only `pn`
# is mocked. `pn workspace update --in-place` is driven via MOCK_PN_UPDATE_*
# (see the base pn mock in setup()) so a test controls the relock's output +
# exit code without the real pn ever running.

# Gives $member's $BRANCH worktree a real upstream via a local bare remote, so
# `git rev-parse --abbrev-ref --symbolic-full-name @{u}` succeeds (the
# no-remote-write guard's has-upstream signal). Members are made by
# _stage_init_member (real canonical repo + real worktree on $BRANCH).
_ur_set_upstream() {
  local member="$1"
  local wt="$SET_DIR/$member"
  local remote="$TEST_DIR/remotes/$member.git"
  command git init -q --bare "$remote"
  command git -C "$wt" remote add origin "$remote"
  command git -C "$wt" push -q -u origin "$BRANCH"
}

@test "update-relock --set exits non-zero on a guard violation" {
  _stage_write_lock repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"not in_workforest"* ]]
}

@test "update-relock --set: a member whose branch has an upstream is refused (no remote write)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  _ur_set_upstream repoA

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"upstream"* ]]
  [[ "$output" == *"refusing"* ]]
  # The relock step must never have run: `pn workspace update` is refused at
  # pre-flight, so a member with an upstream is never pushed.
  [[ "$output" != *"update repoA"* ]]
}

@test "update-relock --set: a member with a tracked change is refused as dirty" {
  _stage_init_member repoA
  _stage_write_lock repoA
  # Modify a TRACKED file (file.txt is committed by _stage_init_member) so it
  # trips `git diff --quiet`; an UNTRACKED file would be allowed (matches pn's
  # isDirty), so this deliberately edits the tracked file.
  echo two >"$SET_DIR/repoA/file.txt"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"dirty"* ]]
}

@test "update-relock --set: an untracked-only member is NOT dirty (matches pn's isDirty)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo scratch >"$SET_DIR/repoA/untracked.txt"
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -eq 0 ]
}

@test "update-relock --set: a relock that SKIPS a repo but exits 0 is reported INCOMPLETE (non-zero)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--
  ⊘ skipping repoA — working tree has uncommitted changes"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"INCOMPLETE"* ]]
  [[ "$output" == *"repoA"* ]]
}

@test "update-relock --set: a relock that SKIPS a repo via 'could not check working tree' but exits 0 is reported INCOMPLETE (non-zero)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=0
  # The SECOND alternation of the backstop regex (pn's other skip line,
  # update.go: "⊘ skipping <name> — could not check working tree: <err>"). Pins
  # both branches of the regex, not just the "uncommitted changes" one above.
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--
  ⊘ skipping repoA — could not check working tree: permission denied"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"INCOMPLETE"* ]]
  [[ "$output" == *"repoA"* ]]
}

@test "update-relock --set: a benign 'skipping update-locks.sh' banner does NOT trip the backstop (exit 0)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=0
  # Mirrors update.go's benign banner (⊘ <name>: no update-locks.sh —
  # skipping): it contains "skipping" but not "⊘ skipping" + a dirty reason,
  # so it must NOT be mistaken for an incomplete relock.
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--
  ⊘ repoA: no update-locks.sh — skipping (workspace inputs already propagated)"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -eq 0 ]
}

@test "update-relock --set: a non-zero relock exit stops with a tail of the output (non-zero)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=1
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--
  ✗ repoA: propagate-edges failed: boom"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"failed"* ]]
  [[ "$output" == *"re-run"* ]]
  # The tail carries the relock's own last output line.
  [[ "$output" == *"propagate-edges failed: boom"* ]]
}

@test "update-relock --set: clean set, no upstream, no skips relocks each member and exits 0" {
  _stage_init_member repoA
  _stage_init_member repoB
  _stage_write_lock repoA repoB
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--
  --== update repoB ==--
  ✓ workspace update finished"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -eq 0 ]
  [[ "$output" == *"workspace update finished"* ]]
}

# CRUX: update-relock MUST clear PN_WORKSPACE_ROOT (env -u) for BOTH the info
# lookup and the relock itself, so a stale value pointing at the canonical
# clones cannot redirect the in-place relock onto them (where the primary
# branch HAS an upstream and would be pushed). Verified via the mock's own
# recorded env: every pn invocation saw PN_WORKSPACE_ROOT unset.
@test "CRUX: update-relock runs pn with PN_WORKSPACE_ROOT unset even when exported to canonical" {
  _stage_init_member repoA
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="  --== update repoA ==--"
  export PN_WORKSPACE_ROOT="$CANONICAL_DIR"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -eq 0 ]

  run cat "$MOCK_PN_ENV_LOG"
  [ "$status" -eq 0 ]
  [[ "$output" == *"PN_WORKSPACE_ROOT=<unset>"* ]]
  [[ "$output" != *"PN_WORKSPACE_ROOT=$CANONICAL_DIR"* ]]
}
