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

# IMMUTABLE, path-stable fixtures are built ONCE per file here (bead pg2-nh1t3):
# the local-dev wrapper and the mock TEMPLATE (the `pn` + default
# integrate-branch-support mocks). Rebuilding them in per-test setup() was pure
# repetition -- their contents never vary across tests, and both mocks resolve
# every per-test path from runtime env (MOCK_PN_ENV_LOG / PN_WORKSPACE_ROOT /
# PWD), never from a value baked in at build time -- so a single shared copy is
# correct even under `bats --jobs`. The per-test MUTABLE state (TEST_DIR, the
# real git repos, and the canned pn-info that embeds those per-test paths) stays
# in setup() so each test remains fully isolated.
setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pnwf-lib.bash"
  fi
  export LIB_PATH

  # Hermetic + fast git. A developer's global `core.fsmonitor=true` makes every
  # throwaway repo these tests `git init` spawn its own fsmonitor daemon that
  # blocks each working-tree op (commit/worktree/status) for 2-3s -- pushing the
  # full suite to ~20min locally (the nix-check sandbox is immune: clean HOME).
  # Inject core.fsmonitor/untrackedcache=false into EVERY git invocation in this
  # test process (GIT_CONFIG_COUNT works like a `-c` flag, so it wins over the
  # inherited global and is surgical -- it does not replace the rest of git
  # config), making the suite fast AND deterministic regardless of whose
  # ~/.gitconfig runs it. Same class of fix as pg2-0sa8p (pn disabling fsmonitor
  # on its own git status probes). Applies to real-git tests; git-mock tests are
  # unaffected. Immutable across tests, so exported once here.
  export GIT_CONFIG_COUNT=2
  export GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=false
  export GIT_CONFIG_KEY_1=core.untrackedcache GIT_CONFIG_VALUE_1=false

  # Full hermeticity on top of that surgical pin (bead pg2-klyn6): the pin above
  # covers only two keys, so EVERY other key still merged in from the developer's
  # ~/.gitconfig, $XDG_CONFIG_HOME/git/config and /etc/gitconfig. /dev/null is the
  # NEUTRAL setting for both scopes, so no test outcome depends on whose machine
  # runs it. Safe here because this suite always pins what it needs explicitly —
  # `git init -q -b <branch>` (never inheriting init.defaultBranch) and repo-local
  # user.email/user.name. Mirrors the pg2-39rz2 Go fix's TestMain in
  # modules/pn/internal/workspace/realgit_test.go. Requires git >= 2.32.
  export GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_SYSTEM=/dev/null

  # Immutable mock TEMPLATE, seeded once. setup() copies these into each test's
  # own MOCK_BIN, so a test may still overwrite its own integrate-branch-support
  # (or drop in a `git` shim) without leaking into sibling tests.
  MOCK_TEMPLATE="$BATS_FILE_TMPDIR/mock-template"
  mkdir -p "$MOCK_TEMPLATE"
  export MOCK_TEMPLATE

  cat >"$MOCK_TEMPLATE/pn" <<'MOCK'
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
  chmod +x "$MOCK_TEMPLATE/pn"

  # Default integrate-branch-support mock (needed by `stage`, via
  # pnwf_resolve_primary_branch): called bare, emits JSON unconditionally.
  cat >"$MOCK_TEMPLATE/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_TEMPLATE/integrate-branch-support"

  # Local dev (no nix-provided SCRIPT_UNDER_TEST): assemble a wrapper replicating
  # the builder's composition (library sourced before the command's .sh) -- see
  # the bash-scripting skill's "Library wrapper pattern". Immutable +
  # read-only-executed, so one per-file wrapper is shared across all tests.
  if [[ -z ${SCRIPT_UNDER_TEST:-} ]]; then
    local resolved_lib
    if [[ -d ${LIB_PATH} ]]; then
      resolved_lib="${LIB_PATH}/pnwf-lib.bash"
    else
      resolved_lib="${LIB_PATH%%:*}"
    fi
    cat >"$BATS_FILE_TMPDIR/pnwf-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${resolved_lib}"
source "${SCRIPTS_DIR}/pnwf.sh"
WRAPPER
    chmod +x "$BATS_FILE_TMPDIR/pnwf-wrapper"
    export SCRIPT_UNDER_TEST="$BATS_FILE_TMPDIR/pnwf-wrapper"
  fi
}

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR

  # Per-test MOCK_BIN seeded from the immutable per-file template (setup_file).
  # Copying rather than rebuilding keeps each test able to overwrite its own
  # mocks without disturbing siblings -- required for `bats --jobs` safety.
  # Mocks live OUTSIDE any git working tree a test creates (pnwf itself never
  # `git clean`s, but this keeps the pattern consistent with the rest of the
  # module -- see testing-advanced.md's mock-isolation gotcha).
  MOCK_BIN="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_BIN"
  cp -p "$MOCK_TEMPLATE/pn" "$MOCK_TEMPLATE/integrate-branch-support" "$MOCK_BIN/"
  PATH="$MOCK_BIN:$PATH"
  export PATH MOCK_BIN

  # HERMETIC HOME (bead pg2-7hr6o), the same three lines the wsplan suites in this
  # module already carry — copied, not reinvented. The bash-scripting skill's
  # test-isolation rule 2 requires it and this suite lacked it, so a bare
  # `bats modules/pnwf/pnwf/tests` read the developer's real HOME for every non-git
  # purpose (the nix check's sandbox HOME hid that: only the gate was hermetic).
  # setup_file's GIT_CONFIG_GLOBAL=/dev/null outranks HOME for GIT alone; caches,
  # XDG defaults, tool configs and credential helpers still resolved off the real
  # one. Per-test (not per-file) so each test gets a pristine, empty HOME.
  HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export HOME

  MOCK_PN_ENV_LOG="$TEST_DIR/pn-env.log"
  : >"$MOCK_PN_ENV_LOG"
  export MOCK_PN_ENV_LOG

  # Canned pn workspace info. Deliberately NOT hoisted to setup_file: it embeds
  # the per-test TEST_DIR paths (root/canonical_root, which tests assert equal
  # to $CANONICAL_DIR/$SET_DIR) and so must be regenerated per test -- hoisting
  # it to a shared per-file file would break the mktemp isolation that makes
  # this suite parallel-safe.
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

@test "fork-preflight: canonical repo with a redirected worktree root -> stop" {
  # The 2026-08-14 homelab defect: a stale `core.worktree` (left by an
  # interrupted `pn workspace update`) redirects every working-tree probe to
  # another directory. The decoy holds an identical checkout so the repo still
  # looks on-primary and clean -- which is what made the real occurrence
  # survive a whole fork -> sync -> validate -> land pipeline.
  _fp_init_canonical_repo repoA
  local decoy="$CANONICAL_DIR/decoy"
  mkdir -p "$decoy"
  cp "$CANONICAL_DIR/repoA/file.txt" "$decoy/file.txt"
  command git -C "$CANONICAL_DIR/repoA" config core.worktree "$decoy"
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  # Reported by check (2), the canonical-READABLE bucket: git is not acting on
  # repoA's own path, so nothing about repoA's state was established.
  [[ "$output" == *"could not read the canonical state"* ]]
  [[ "$output" == *"repoA"* ]]
  # The relayed detail must stay actionable: it names the likely cause and the
  # directory git actually resolved to (basename only -- the absolute form
  # differs by the /var -> /private/var symlink on darwin).
  [[ "$output" == *"core.worktree"* ]]
  [[ "$output" == *"decoy"* ]]
  # It MUST NOT be reported as the on-primary/clean failure -- sending the
  # operator to inspect a working tree whose state is not the problem is the
  # specific misdiagnosis this check exists to prevent. Non-vacuous: the lib
  # test "the redirect is INVISIBLE to the on-primary/clean check" pins that
  # that check really does report healthy here.
  [[ "$output" != *"not clean/on-primary"* ]]
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

@test "fork-preflight: a canonical path that is not a git repo stops, and is NOT called dirty" {
  # The member path EXISTS and is not a git repo. It must be diagnosed as
  # unreadable, and the reason MUST NOT blame it for being off-primary or dirty
  # -- neither was established (bd pg2-xc9b7).
  _fp_init_canonical_repo repoA
  mkdir -p "$CANONICAL_DIR/repoB"
  _fp_write_canonical_info repoA repoB
  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" == *"could not read the canonical state"* ]]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" != *"not clean/on-primary"* ]]
}

@test "fork-preflight: a canonical path NESTED in an enclosing repo stops rather than proceeding" {
  # THE CRUX. `git -C <path>` WALKS UP, so before the root confirmation every
  # check answered for the ENCLOSING repository -- exit 0, no diagnostic -- and
  # this printed `proceed`, the fork stage's go-ahead, for a canonical checkout
  # it had never read (bd pg2-xc9b7). The enclosing repo is made CLEAN and on
  # `main` on purpose: that is what made the old code's checks all pass.
  #
  # git config is set explicitly in the fixture repo (the harness is hermetic
  # against ambient config and HOME -- pg2-klyn6/pg2-7hr6o), and `git status` is
  # given `--untracked-files=no` here for the same reason: the assertion must not
  # depend on `status.showUntrackedFiles`.
  command git -C "$CANONICAL_DIR" init -q -b main
  command git -C "$CANONICAL_DIR" config user.email "test@example.com"
  command git -C "$CANONICAL_DIR" config user.name "Test"
  mkdir -p "$CANONICAL_DIR/repoA"
  echo placeholder >"$CANONICAL_DIR/repoA/.keep"
  _fp_write_canonical_info repoA
  command git -C "$CANONICAL_DIR" add -A
  command git -C "$CANONICAL_DIR" commit -q -m "enclosing repo, clean"
  [ -z "$(command git -C "$CANONICAL_DIR" status --porcelain --untracked-files=no)" ]

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" != *"proceed"* ]]
  [[ "$output" == *"repoA"* ]]
  [[ "$output" == *"ENCLOSING repository"* ]]
}

@test "fork-preflight: an enclosing repo's branch does not make a nested path 'resume'" {
  # The other direction of the same walk-up: the branch exists ONLY in the
  # enclosing repo, and the old boolean reported it as the member's, printing
  # `resume` -- which sends the caller into a resume-vs-discard decision about a
  # set that does not exist. It must stop instead.
  command git -C "$CANONICAL_DIR" init -q -b main
  command git -C "$CANONICAL_DIR" config user.email "test@example.com"
  command git -C "$CANONICAL_DIR" config user.name "Test"
  mkdir -p "$CANONICAL_DIR/repoA"
  echo placeholder >"$CANONICAL_DIR/repoA/.keep"
  _fp_write_canonical_info repoA
  command git -C "$CANONICAL_DIR" add -A
  command git -C "$CANONICAL_DIR" commit -q -m "enclosing repo, clean"
  command git -C "$CANONICAL_DIR" branch new-feature

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" != *"resume"* ]]
}

@test "fork-preflight: a repo whose git state cannot be read stops, and is NOT called off-primary or dirty" {
  # The path IS a working-tree root, so check (2)'s root confirmation passes; it
  # is the REF STORE git cannot parse, which makes `symbolic-ref` and `status`
  # both exit 128, so `pnwf_canonical_on_primary_and_clean` returns 128 rather
  # than its established 1. That must be reported as unreadable, not as
  # "expected on 'main', clean" -- the conflation one probe over from the branch
  # one (bd pg2-xc9b7).
  #
  # An unparseable `packed-refs` is the corruption that does it: verified git
  # 2.54, a branch-name/directory conflict, a self-symlink and a dangling symref
  # all still exit 1 ("absent"). A corrupt FILE rather than a chmod, so the
  # outcome does not depend on the build user (pg2-deonn's reasoning).
  #
  # `--separate-stderr` is required: git's own `fatal:` and pnwf's first-party
  # diagnostic go to stderr, which bats' default `run` would merge into
  # `${lines[0]}` ahead of the outcome token.
  _fp_init_canonical_repo repoA
  command git -C "$CANONICAL_DIR/repoA" pack-refs --all
  printf 'garbage line\n' >"$CANONICAL_DIR/repoA/.git/packed-refs"
  _fp_write_canonical_info repoA
  cd "$CANONICAL_DIR"
  run --separate-stderr "$SCRIPT_UNDER_TEST" fork-preflight new-feature
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "stop" ]
  [[ "$output" != *"proceed"* ]]
  [[ "$output" == *"repoA"* ]]
  [[ "$output" == *"could not read the canonical state"* ]]
  [[ "$output" != *"not clean/on-primary"* ]]
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

@test "cleanup: stops a landed member's fsmonitor daemon immediately before removing its worktree (pg2-fnjfs)" {
  _stage_init_member repoA
  _stage_write_lock repoA
  echo two >"$SET_DIR/repoA/file.txt"
  command git -C "$SET_DIR/repoA" commit -q -am second
  command git -C "$CANONICAL_DIR/repoA" merge -q "$BRANCH"

  # Install a git shim that logs each invocation's args (one line per call)
  # then delegates to the real git by absolute path, so we can assert pnwf
  # issues a best-effort `fsmonitor--daemon stop` on the worktree IMMEDIATELY
  # BEFORE removing it. The per-worktree fsmonitor daemon is keyed by worktree
  # path and is NOT torn down by `git worktree remove`, so skipping the stop
  # orphans it (bead pg2-fnjfs). real_git is resolved before the shim exists,
  # so it points at the real binary (MOCK_BIN has no git yet).
  local real_git
  real_git="$(command -v git)"
  cat >"$MOCK_BIN/git" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$TEST_DIR/git-calls.log"
exec "$real_git" "\$@"
SHIM
  chmod +x "$MOCK_BIN/git"

  cd "$CANONICAL_DIR"
  run "$SCRIPT_UNDER_TEST" cleanup "$BRANCH"
  [ "$status" -eq 0 ]
  [[ "$output" == *"repoA"$'\t'"removed"* ]]

  # Only one member, so exactly one stop and one remove line are logged.
  local stop_line remove_line
  stop_line=$(grep -nF -- "fsmonitor--daemon stop" "$TEST_DIR/git-calls.log" | head -1 | cut -d: -f1)
  remove_line=$(grep -nF -- "worktree remove" "$TEST_DIR/git-calls.log" | head -1 | cut -d: -f1)
  [ -n "$stop_line" ]
  [ -n "$remove_line" ]
  # The stop must target repoA's set worktree, and be issued IMMEDIATELY before
  # (no git call between) the worktree remove.
  grep -qF -- "-C $SET_DIR/repoA fsmonitor--daemon stop" "$TEST_DIR/git-calls.log"
  [ "$remove_line" -eq $((stop_line + 1)) ]
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
# MID-REBASE vs. REFUSED-REBASE return-code distinctions
# pnwf_fetch_and_rebase itself signals, and the `rev-parse --git-path` probe
# it derives the latter two from) are proven with REAL git -- including a
# real linked worktree -- directly in test-pnwf-lib.bats. Member dirs are
# plain directories (no `.git` needed): sync-fetch's own git calls are
# fully mocked, and pnwf_resolve_primary_branch only needs `cd` into
# member_canonical before calling the (already-mocked)
# integrate-branch-support.
#
# The mock logs every invocation as "<dir> <subcommand>" to MOCK_GIT_LOG so
# tests can assert both WHICH members were touched and in what order --
# proving the loop stops at the first failure rather than merely reporting
# it while continuing underneath. A failed rebase now also logs the
# `rev-parse` probe that classifies it (see below), so the log is likewise
# the evidence that the mid-rebase-vs-refused verdict came from an
# observable rather than from a catch-all.
#
# FIVE distinguishable stopping points are modelled, because
# `pnwf_fetch_and_rebase` returns a different sentinel for each and this
# subcommand must not collapse them (bd pg2-k3s0x, bd pg2-lgzcg):
#   .mock-rebase-conflict     rebase STARTS and stops mid-way -- the mock
#                             creates the rebase state directory git itself
#                             would leave behind, so the probe reports
#                             in-progress (sentinel 3).
#   .mock-rebase-refused      rebase is REFUSED outright and creates NO state
#                             directory, so the probe reports not-in-progress
#                             (sentinel 4).
#   .mock-rev-parse-failure   the probe itself cannot read the observable, so
#                             neither verdict may be asserted (sentinel 5).
#   .mock-dirty               `status --porcelain` reports an uncommitted
#                             change, so the PRE-CHECK refuses the member
#                             before the fetch (sentinel 6).
#   .mock-status-failure      `status --porcelain` itself fails, so whether a
#                             rebase is safe cannot be read (sentinel 7).
#
# The mocked `status --porcelain` is SYNTHETIC -- driven only by the two
# markers above, never by what is actually on disk. That is deliberate: the
# marker files are themselves untracked files inside the member directory, so
# a real `status` would report EVERY marked member as dirty and the pre-check
# would swallow the rebase cases this suite exists to pin.
#
# `rev-parse --git-path <name>` answers with an ABSOLUTE path under a
# per-member `.mock-gitdir`, mirroring the LINKED-WORKTREE shape a real set
# member has (where the state directory lives in the canonical clone's
# .git/worktrees/<name>/, never in <member>/.git).

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
status)
  if [[ -f "$dir/.mock-status-failure" ]]; then
    echo "mock git: fatal: not a git repository" >&2
    exit 128
  fi
  if [[ -f "$dir/.mock-dirty" ]]; then
    echo " M file.txt"
  fi
  exit 0
  ;;
fetch)
  if [[ -f "$dir/.mock-fetch-failure" ]]; then
    echo "mock git: fatal: could not read from remote repository" >&2
    exit 128
  fi
  exit 0
  ;;
rebase)
  if [[ -f "$dir/.mock-rebase-conflict" ]]; then
    mkdir -p "$dir/.mock-gitdir/rebase-merge"
    echo "mock git: CONFLICT (content): Merge conflict in file.txt" >&2
    exit 1
  fi
  if [[ -f "$dir/.mock-rebase-refused" ]]; then
    echo "mock git: error: cannot rebase: You have unstaged changes." >&2
    exit 1
  fi
  exit 0
  ;;
rev-parse)
  if [[ "${2:-}" != "--git-path" ]]; then
    echo "mock git: unsupported rev-parse invocation: $dir $*" >&2
    exit 1
  fi
  if [[ -f "$dir/.mock-rev-parse-failure" ]]; then
    echo "mock git: fatal: not a git repository" >&2
    exit 128
  fi
  echo "$dir/.mock-gitdir/${3:?mock git: --git-path requires a name}"
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
  # THREE calls per member, and `status` is FIRST: the dirtiness pre-check
  # precedes the fetch, so a member that is already a decided stop costs no
  # network round trip (bd pg2-lgzcg).
  [ "${#lines[@]}" -eq 9 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
  [ "${lines[4]}" = "$SET_DIR/repoB fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoB rebase" ]
  [ "${lines[6]}" = "$SET_DIR/repoC status" ]
  [ "${lines[7]}" = "$SET_DIR/repoC fetch" ]
  [ "${lines[8]}" = "$SET_DIR/repoC rebase" ]
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
  # The EXIT STATUS is contract, not incidental: `pnwf sync-fetch --help`
  # enumerates it and the pnwf-runner agent is required to classify on it
  # rather than on this wording, so each case pins its own status.
  [ "$status" -eq 3 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"$SET_DIR/repoB"* ]]

  # Rebase-specific recovery wording: a conflict DID leave a rebase in
  # progress, so `git rebase --continue` is correct advice here (contrast
  # with the fetch-failure and refused-rebase tests below, where it would be
  # wrong advice).
  [[ "$output" == *"rebase --continue"* ]]
  [[ "$output" != *"'git fetch origin' failed"* ]]
  [[ "$output" != *"REFUSED"* ]]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 7 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  # repoB's pre-check passes (it is not dirty), so this IS a rebase-stage
  # failure rather than the pre-check's own refusal.
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
  [ "${lines[4]}" = "$SET_DIR/repoB fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoB rebase" ]
  # The classifying probe -- the failed rebase is diagnosed by ASKING git
  # whether a rebase is in progress, not by assuming one is. It runs exactly
  # once here: `rebase-merge` is found, so `rebase-apply` is never queried.
  [ "${lines[6]}" = "$SET_DIR/repoB rev-parse" ]
  [[ "$output" != *"repoC"* ]]
}

@test "sync-fetch --set: a REFUSED rebase on a CLEAN tree reports git's own refusal, NEVER 'rebase --continue', exits non-zero, and does not continue" {
  # bd pg2-k3s0x. `git rebase` refuses without starting, so nothing is
  # mid-rebase and the mid-rebase hand-off would send the operator to
  # `git rebase --continue` for a rebase that never existed. Before the fix
  # a catch-all `*)` branch asserted exactly that.
  #
  # bd pg2-lgzcg narrowed WHICH causes land here: this member is NOT dirty
  # (no `.mock-dirty`), so the pre-check passes it through and the refusal is
  # git's own -- an unresolvable upstream or a `pre-rebase` hook veto, both
  # verified on git 2.54 as exit non-zero with no rebase state directory. A
  # DIRTY member never reaches this code path at all; that is sentinel 6.
  _stage_write_lock repoA repoB repoC
  _sync_fetch_init_members repoA repoB repoC
  touch "$SET_DIR/repoB/.mock-rebase-refused"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  # 4, DISTINCT from the mid-rebase 3 above: this status is what the
  # pnwf-runner agent classifies on to pick the `rebase-refused` gate over
  # the `rebase-conflict` one.
  [ "$status" -eq 4 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"$SET_DIR/repoB"* ]]

  # The whole point of pg2-k3s0x: the mid-rebase hand-off MUST NOT appear --
  # not even the claim that the member IS mid-rebase. The `rebase --continue`
  # string is asserted absent OUTRIGHT (rather than "absent as advice"),
  # because the pnwf-runner agent classifies this stderr and would key on it
  # wherever it appeared.
  [[ "$output" != *"rebase --continue"* ]]
  [[ "$output" != *"mid-rebase"* ]]
  [[ "$output" == *"REFUSED"* ]]
  [[ "$output" != *"'git fetch origin' failed"* ]]

  # pg2-lgzcg: this message must no longer offer the DIRTY-tree recovery, and
  # must no longer call a dirty worktree the classic cause. With
  # `rebase.autoStash` on, a dirty tree does not make git refuse at all, so
  # "commit or stash" here was advice for a stop that would not occur -- and
  # the runner classifies this stderr, so a stale string is not inert. That
  # recovery now belongs to sentinel 6 alone.
  [[ "$output" != *"commit or stash"* ]]
  [[ "$output" != *"classic cause"* ]]
  # And it says what actually refused instead, on a tree it confirmed clean.
  [[ "$output" == *"CLEAN"* ]]
  [[ "$output" == *"pre-rebase"* ]]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 8 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  # repoB's pre-check runs and PASSES (not dirty), so the fetch and rebase
  # are both attempted -- the evidence that "CLEAN" above is observed rather
  # than asserted.
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
  [ "${lines[4]}" = "$SET_DIR/repoB fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoB rebase" ]
  # BOTH backends are queried before concluding "no rebase in progress" --
  # `rebase-merge` then `rebase-apply`; concluding it off one would miss a
  # rebase run with the apply/am backend.
  [ "${lines[6]}" = "$SET_DIR/repoB rev-parse" ]
  [ "${lines[7]}" = "$SET_DIR/repoB rev-parse" ]
  [[ "$output" != *"repoC"* ]]
}

@test "sync-fetch --set: a DIRTY member is refused BEFORE any fetch or rebase, with its own status and recovery, and does not continue" {
  # bd pg2-lgzcg, the CLI half. This member never reaches `git rebase`, so
  # the outcome cannot depend on `rebase.autoStash` -- which is the whole
  # point: with it ON git reports SUCCESS for a dirty member (having possibly
  # left a conflicted autostash), so exit 4's "REFUSED" path could never fire
  # for the cause its own message used to call classic.
  _stage_write_lock repoA repoB repoC
  _sync_fetch_init_members repoA repoB repoC
  touch "$SET_DIR/repoB/.mock-dirty"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  # 6: its own status, DISTINCT from 2/3/4/5, so the runner picks the
  # dirty-member gate rather than either rebase gate.
  [ "$status" -eq 6 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"$SET_DIR/repoB"* ]]

  # The recovery that actually applies -- and it is the ONLY message that
  # carries it now.
  [[ "$output" == *"UNCOMMITTED CHANGES"* ]]
  [[ "$output" == *"commit or stash"* ]]

  # The mid-rebase commands are absent OUTRIGHT: not as advice, not as a
  # negation, not named at all. Nothing was started here, and the runner
  # classifies this stderr -- a naive matcher keying on either string would
  # re-emit a rebase-conflict hand-off for a member that never rebased.
  [[ "$output" != *"rebase --continue"* ]]
  [[ "$output" != *"rebase --abort"* ]]
  [[ "$output" != *"mid-rebase"* ]]
  # Distinguishable from sentinel 4 by WORDING as well as by status, so a
  # human reading stderr cannot confuse pnwf's own refusal with git's.
  [[ "$output" != *"REFUSED"* ]]
  [[ "$output" != *"'git fetch origin' failed"* ]]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  # FOUR lines, and this is the discriminating evidence: repoB's `status` is
  # logged but its `fetch` and `rebase` are NOT -- nothing was attempted --
  # and repoC is never reached.
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
  [[ "$output" != *"repoC"* ]]
}

@test "sync-fetch --set: when the dirtiness observable cannot be read, NO recovery is asserted and nothing is attempted" {
  # Same discipline as the unreadable rebase-in-progress observable (5), one
  # step earlier: the pre-check could not read whether a rebase is SAFE, so
  # neither the dirty-member recovery nor any rebase recovery may be claimed.
  _stage_write_lock repoA repoB
  _sync_fetch_init_members repoA repoB
  touch "$SET_DIR/repoB/.mock-status-failure"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  # 7: its own status, so the runner halts rather than picking any gate.
  [ "$status" -eq 7 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"could not be determined"* ]]
  [[ "$output" != *"commit or stash"* ]]
  [[ "$output" != *"rebase --continue"* ]]

  run cat "$MOCK_GIT_LOG"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 4 ]
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
}

@test "sync-fetch --set: when the rebase-in-progress observable cannot be read, NO recovery is asserted" {
  # Neither hand-off may be given when the classifying probe itself fails:
  # asserting one would be the same defect as the old catch-all, just with a
  # different guess.
  _stage_write_lock repoA repoB
  _sync_fetch_init_members repoA repoB
  touch "$SET_DIR/repoB/.mock-rebase-refused"
  touch "$SET_DIR/repoB/.mock-rev-parse-failure"

  MOCK_GIT_LOG="$TEST_DIR/git.log"
  : >"$MOCK_GIT_LOG"
  export MOCK_GIT_LOG
  _sync_fetch_write_git_mock

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" sync-fetch --set
  # 5: its own status, so the runner halts rather than picking either gate.
  [ "$status" -eq 5 ]
  [[ "$output" == *"repoB"* ]]
  [[ "$output" == *"could not be determined"* ]]
  [[ "$output" != *"rebase --continue"* ]]
  [[ "$output" != *"commit or stash"* ]]
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
  # 2: the FETCH-step status, distinct from both rebase statuses (3 and 4).
  [ "$status" -eq 2 ]
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
  [ "${#lines[@]}" -eq 5 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  # repoB's pre-check passes, so the fetch IS attempted -- and stops there.
  [ "${lines[3]}" = "$SET_DIR/repoB status" ]
  [ "${lines[4]}" = "$SET_DIR/repoB fetch" ]
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
  [ "${#lines[@]}" -eq 6 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[3]}" = "$SET_DIR/repoC status" ]
  [ "${lines[4]}" = "$SET_DIR/repoC fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoC rebase" ]
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
  [ "${#lines[@]}" -eq 6 ]
  [ "${lines[0]}" = "$SET_DIR/repoA status" ]
  [ "${lines[1]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[2]}" = "$SET_DIR/repoA rebase" ]
  [ "${lines[3]}" = "$SET_DIR/repoA status" ]
  [ "${lines[4]}" = "$SET_DIR/repoA fetch" ]
  [ "${lines[5]}" = "$SET_DIR/repoA rebase" ]
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

@test "update-relock --set: a member path that EXISTS but is not a git repo gets ONE honest diagnosis" {
  # bd pg2-deonn, the CLI half, and the input on which the two pre-flight guards
  # used to fail in OPPOSITE directions: the no-remote-write guard waved this
  # member through silently (it read rev-parse's 128 as "no upstream", the
  # REQUIRED state) while the cleanliness guard then blamed "a prior failed
  # relock". `pnwf_worktree_present` is a plain `-e` check, so this state is
  # reachable, not theoretical.
  #
  # No _stage_init_member: the directory exists and is NOT a git repo.
  mkdir -p "$SET_DIR/repoA"
  _stage_write_lock repoA
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="RELOCK-RAN"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"repoA"* ]]
  [[ "$output" == *"$SET_DIR/repoA"* ]]

  # The guard that answers is the no-remote-write one, and it FAILS CLOSED:
  # "could not tell" is refused rather than treated as the required state.
  [[ "$output" == *"could NOT be determined"* ]]
  [[ "$output" == *"fail CLOSED"* ]]

  # ONE diagnosis, not two contradictory ones: the cleanliness guard's claims
  # are absent OUTRIGHT. "dirty" is asserted absent as a bare string (not merely
  # as advice) because consumers classify this stderr -- /pn-workspace-update
  # keys an update halt's recovery on whether the message names dirty residue,
  # and would send the operator to disposition residue that was never observed.
  [[ "$output" != *"dirty"* ]]
  [[ "$output" != *"prior failed relock"* ]]

  # And the relock itself never ran, so nothing could have been pushed.
  [[ "$output" != *"RELOCK-RAN"* ]]
}

@test "update-relock --set: a confirmed worktree whose cleanliness probe cannot run is refused with NO cause asserted" {
  # The cleanliness guard's own third leg, reachable only PAST the repo
  # confirmation above: a real worktree whose `git status` cannot run. Corrupting
  # the index does exactly that (verified git 2.54: "index file smaller than
  # expected", rc 128) while leaving `rev-parse --show-prefix` -- which reads no
  # index -- answering 0, so guard 1 passes the member and guard 2 is the one
  # that must classify it. Deliberately NOT a chmod: a mode-based refusal would
  # depend on the build user, and this suite must behave the same in the nix
  # check sandbox as locally.
  _stage_init_member repoA
  _stage_write_lock repoA
  printf 'not-an-index' >"$(command git -C "$SET_DIR/repoA" rev-parse --git-path index)"
  export MOCK_PN_UPDATE_RC=0
  export MOCK_PN_UPDATE_OUTPUT="RELOCK-RAN"

  cd "$SET_DIR"
  run "$SCRIPT_UNDER_TEST" update-relock --set
  [ "$status" -ne 0 ]
  [[ "$output" == *"repoA"* ]]
  [[ "$output" == *"could NOT be determined"* ]]

  # rc 128 is NOT rc 1: neither claim of the rc-1 message survives here, and
  # both are absent as BARE STRINGS rather than as negations -- the same
  # drafting rule sync-fetch's sentinels 4/6 follow, because consumers classify
  # this stderr and would key on either string wherever it appeared.
  [[ "$output" == *"NO cause is asserted"* ]]
  [[ "$output" != *"worktree is dirty"* ]]
  [[ "$output" != *"prior failed relock"* ]]
  # (The bare word "dirty" cannot be asserted absent HERE, unlike in the
  # non-repo test above: the guarded probe's own first-party diagnostic names
  # the function -- "pnwf_working_tree_dirty: git status failed (rc=128)" --
  # and that line is the evidence the guard's error path ran rather than
  # errexit aborting it. It says FAILED, not dirty.)
  [[ "$output" == *"pnwf_working_tree_dirty: git status failed"* ]]
  # It is the CLEANLINESS probe that could not answer, not the upstream one --
  # guard 1 confirmed the repo and found no upstream.
  [[ "$output" == *"TRACKED changes"* ]]
  [[ "$output" != *"has an upstream"* ]]
  [[ "$output" != *"REMOTE WRITE"* ]]

  [[ "$output" != *"RELOCK-RAN"* ]]
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

# --- harness hermeticity guard ----------------------------------------------

@test "setup() relocates HOME off the developer's own (pg2-7hr6o regression guard)" {
  # Discriminating guard for the HOME isolation setup() installs, mirroring the
  # one in lib/tests/test-update-locks-lib.bats: drop `export HOME` from setup()
  # and HOME is the developer's real one, so the equality fails; drop the whole
  # block and $TEST_DIR/home does not exist, so the -d fails. Either way it goes
  # red, which is what separates a guard from a restatement.
  #
  # The real ~ is never touched, read, or probed: the assertions look only at the
  # temp dir setup() created, and a fresh mktemp path can never BE the
  # developer's home, so proving isolation needs no reference to the real path.
  [ -n "${TEST_DIR:-}" ]
  [ "$HOME" = "$TEST_DIR/home" ]
  [ -d "$HOME" ]
  # Empty, i.e. nothing of the developer's is reachable through $HOME.
  [ -z "$(ls -A "$HOME")" ]
}
