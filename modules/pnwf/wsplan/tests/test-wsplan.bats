#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Integration suite for the `wsplan` CLI: every test drives the ASSEMBLED
# artifact via SCRIPT_UNDER_TEST as a SUBPROCESS. In the nix check that is the
# real wrapped binary; for a local `bats tests/` run (no nix build), setup_file
# assembles an equivalent wrapper that sources pnwf-lib.bash, then wsplan.bash,
# then wsplan.sh in the same order the builder composes them — so this suite is
# genuinely RED before wsplan.sh exists and GREEN once it is implemented,
# rather than merely skipped locally.
#
# `pn`, `pnwf` and `integrate-branch-support` are ALL mocked, never the real
# binaries. The bats check's PATH is only
# `[ bats bash ] ++ optional (batsJobs > 1) parallel ++ testDeps`
# (lib/bash-builders.nix:370-376), so `pn` and `integrate-branch-support` are
# simply ABSENT there; `pnwf` IS a declared runtimeDep, but runtimeDeps are
# appended with `--suffix`, so MOCK_BIN (prepended to PATH) still wins.
#
# git is REAL throughout: the two defects these tests exist to catch — a
# canonical-only enumeration, and a missing work-area-to-repo reduction — are
# both about what git actually reports, so mocking git would make the suite
# agree with whichever mistake the implementation made.
#
# `run --separate-stderr` is used wherever an envelope is parsed, so `$output`
# is stdout ONLY and "exactly one JSON object on stdout" is actually asserted
# rather than assumed.
#
# The version output is NOT tested: the builder injects it.

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pnwf-lib.bash"
  fi
  export LIB_PATH

  # Hermetic + fast git. A global core.fsmonitor=true would make every
  # throwaway repo spawn a daemon that blocks each working-tree op for seconds;
  # GIT_CONFIG_COUNT acts like a `-c` flag so it wins over the inherited global
  # and is surgical. Performance-only, so behavior-neutral.
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
  # own MOCK_BIN, so a test may overwrite its own mock without leaking into
  # siblings — required for `bats --jobs` safety. Every mock resolves per-test
  # state from RUNTIME env (PWD / PN_WORKSPACE_ROOT / MOCK_*), never from a
  # value baked in at template-build time.
  MOCK_TEMPLATE="$BATS_FILE_TMPDIR/mock-template"
  mkdir -p "$MOCK_TEMPLATE"
  export MOCK_TEMPLATE

  # `pn`: answers `workspace info --json` by walking UP from
  # ${PN_WORKSPACE_ROOT:-$PWD} for a `.mock-pn-info.json` marker, mirroring
  # pn's own PN_WORKSPACE_ROOT-then-cwd precedence. Because wsplan pins cwd to
  # --root and clears PN_WORKSPACE_ROOT, the walk starts at --root — which is
  # what makes the cwd-pinning test below meaningful.
  cat >"$MOCK_TEMPLATE/pn" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail

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
  chmod +x "$MOCK_TEMPLATE/pn"

  # `pnwf`: only `land-plan <branch>` is delegated to. Prints
  # MOCK_PNWF_LAND_PLAN_OUTPUT and exits MOCK_PNWF_LAND_PLAN_RC (default 0), so
  # a test can drive an empty set, a populated set, a non-zero delegate and an
  # unusable-output delegate without the real pnwf. It also records its own cwd
  # so the §4 cwd-pinning rule is observable.
  cat >"$MOCK_TEMPLATE/pnwf" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${MOCK_PNWF_CWD_LOG:-}" ]]; then
  echo "$PWD" >>"$MOCK_PNWF_CWD_LOG"
fi

if [[ "${1:-}" == "land-plan" ]]; then
  if [[ -n "${MOCK_PNWF_LAND_PLAN_OUTPUT:-}" ]]; then
    printf '%s\n' "$MOCK_PNWF_LAND_PLAN_OUTPUT"
  fi
  exit "${MOCK_PNWF_LAND_PLAN_RC:-0}"
fi

echo "mock pnwf: unsupported invocation: $*" >&2
exit 1
MOCK
  chmod +x "$MOCK_TEMPLATE/pnwf"

  # `integrate-branch-support`: called bare (no flags), emits JSON
  # unconditionally, matching the real tool. MOCK_PRIMARY_BRANCH lets a test
  # drive a different primary; MOCK_IBS_RC lets a test drive the delegate
  # failure that pnwf_resolve_primary_branch relays.
  cat >"$MOCK_TEMPLATE/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
if [[ "${MOCK_IBS_RC:-0}" != "0" ]]; then
  echo "mock integrate-branch-support: forced failure" >&2
  exit "${MOCK_IBS_RC}"
fi
printf '{"primary_branch":"%s","strategy":null}\n' "${MOCK_PRIMARY_BRANCH:-main}"
MOCK
  chmod +x "$MOCK_TEMPLATE/integrate-branch-support"

  # Local dev (no nix-provided SCRIPT_UNDER_TEST): assemble a wrapper
  # replicating the builder's composition order — libraries, then <name>.bash,
  # then <name>.sh.
  if [[ -z ${SCRIPT_UNDER_TEST:-} ]]; then
    local resolved_lib
    if [[ -d ${LIB_PATH} ]]; then
      resolved_lib="${LIB_PATH}/pnwf-lib.bash"
    else
      resolved_lib="${LIB_PATH%%:*}"
    fi
    cat >"$BATS_FILE_TMPDIR/wsplan-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${resolved_lib}"
source "${SCRIPTS_DIR}/wsplan.bash"
source "${SCRIPTS_DIR}/wsplan.sh"
WRAPPER
    chmod +x "$BATS_FILE_TMPDIR/wsplan-wrapper"
    export SCRIPT_UNDER_TEST="$BATS_FILE_TMPDIR/wsplan-wrapper"
  fi
}

setup() {
  # `pwd -P`: git reports PHYSICAL paths (macOS's /var is a symlink into
  # /private/var), and wsplan normalizes --root the same way, so a fixture built
  # on the unresolved path would compare unequal to git's own output.
  TEST_DIR="$(cd "$(mktemp -d)" && pwd -P)"
  export TEST_DIR

  MOCK_BIN="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_BIN"
  cp -p "$MOCK_TEMPLATE/pn" "$MOCK_TEMPLATE/pnwf" \
    "$MOCK_TEMPLATE/integrate-branch-support" "$MOCK_BIN/"
  PATH="$MOCK_BIN:$PATH"
  export PATH MOCK_BIN

  HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export HOME

  MOCK_PNWF_CWD_LOG="$TEST_DIR/pnwf-cwd.log"
  : >"$MOCK_PNWF_CWD_LOG"
  export MOCK_PNWF_CWD_LOG

  # Default: no edges, and the default relative workforests_dir. A test that
  # needs the edge test sets WS_EDGES, and the absolute-layout test sets
  # WS_WORKFORESTS_DIR, before calling _ws_write_lock / _ws_write_info.
  WS_EDGES='[]'
  unset WS_WORKFORESTS_DIR
  WS="$TEST_DIR/ws"
  export WS WS_EDGES
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- fixture helpers --------------------------------------------------------

# The canonical workspace root: a pn-workspace.toml (which is all the §5.2 Q2
# walk looks for — wsplan never parses it) plus somewhere for the canned pn
# info and lock to live.
_ws_init() {
  mkdir -p "$WS"
  printf '[workspace]\nid = "test-ws"\nterminal = "a"\n' >"$WS/pn-workspace.toml"
}

# A real member repo at $WS/<name> with one commit on <branch> (default main).
_ws_member() {
  local name="$1" branch="${2:-main}"
  local dir="$WS/$name"
  mkdir -p "$dir"
  command git -C "$dir" init -q -b "$branch"
  command git -C "$dir" config user.email "test@example.com"
  command git -C "$dir" config user.name "Test"
  echo one >"$dir/file.txt"
  command git -C "$dir" add file.txt
  command git -C "$dir" commit -q -m initial
}

# A linked worktree of member <name> at <path> on a NEW branch <branch>,
# carrying one commit so it is genuinely ahead of primary (unlanded).
_ws_unlanded() {
  local name="$1" path="$2" branch="$3"
  command git -C "$WS/$name" worktree add -q "$path" -b "$branch"
  echo work >"$path/work.txt"
  command git -C "$path" add work.txt
  command git -C "$path" commit -q -m "work on $branch"
}

# The default work-area location for member <name>: an in-repo
# `.worktrees/<branch>`, mirroring how this repo actually lays worktrees out —
# which also exercises the deepest-match containment rule, since the member
# canonical and its worktree BOTH contain such a --root.
_ws_unlanded_default() {
  local name="$1" branch="$2"
  _ws_unlanded "$name" "$WS/$name/.worktrees/$branch" "$branch"
}

# Canned `pn workspace info --json` at the workspace root. WS_WORKFORESTS_DIR
# overrides workforests_dir (used by the absolute-layout refusal test).
_ws_write_info() {
  local repos_json n
  repos_json=$(
    for n in "$@"; do
      jq -n --arg n "$n" --arg p "$WS/$n" \
        '{name:$n, path:$p, applied_ref:"", dirty:false}'
    done | jq -s .
  )
  jq -n \
    --arg root "$WS" \
    --arg wf "${WS_WORKFORESTS_DIR:-.workforests}" \
    --argjson repos "$repos_json" \
    '{
      wsid: "test-ws",
      root: $root,
      terminal: "a",
      workforests_dir: $wf,
      in_workforest: false,
      canonical_root: $root,
      repos: $repos
    }' >"$WS/.mock-pn-info.json"
}

# The canonical workspace lock. Order is the argument list; edges come from
# WS_EDGES so a test can wire the §5.6 graph it needs.
_ws_write_lock() {
  local order_json
  order_json=$(printf '%s\n' "$@" | jq -R . | jq -s .)
  jq -n --argjson order "$order_json" --argjson edges "$WS_EDGES" \
    '{order: $order, terminal: ($order | last), repos: {}, edges: $edges}' \
    >"$WS/pn-workspace.lock.json"
}

# A coordinated set directory at $WS/.workforests/<branch>, carrying its OWN
# pn-workspace.toml and its own in_workforest=true canned info — which is
# exactly why §5.2's Q2B test is required: without it a --root in here would be
# mistaken for a workspace root.
_ws_set_dir() {
  local branch="$1"
  local setdir="$WS/.workforests/$branch"
  mkdir -p "$setdir"
  printf '[workspace]\nid = "test-ws"\n' >"$setdir/pn-workspace.toml"
  jq -n --arg root "$setdir" --arg canonical "$WS" '{
    wsid: "test-ws",
    root: $root,
    terminal: "a",
    workforests_dir: ".workforests",
    in_workforest: true,
    canonical_root: $canonical,
    repos: []
  }' >"$setdir/.mock-pn-info.json"
  printf '%s\n' "$setdir"
}

# A member checkout inside the set, mirroring pn's own WorkforestAdd.
_ws_set_member() {
  local branch="$1" name="$2"
  _ws_unlanded "$name" "$WS/.workforests/$branch/$name" "$branch"
}

# The single-member workspace most tests want: member <name>, info, lock.
_ws_simple() {
  local name="${1:-a}"
  _ws_init
  _ws_member "$name"
  _ws_write_info "$name"
  _ws_write_lock "$name"
}

# --- §7 row 1: nothing to do ------------------------------------------------

@test "row 1: workspace root with no unlanded work reports nothing-to-do at shape workspace" {
  _ws_simple a
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
  [ "$(echo "$output" | jq -r '.shape')" = "workspace" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
}

@test "row 1: a landed worktree is not a target" {
  _ws_init
  _ws_member a
  # a worktree on a fresh branch with NO commit is an ancestor of main
  command git -C "$WS/a" worktree add -q "$WS/a/.worktrees/idle" -b idle
  _ws_write_info a
  _ws_write_lock a
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
}

# --- §7 row 2: single repo --------------------------------------------------

@test "row 2 / linked-worktree discovery: work living ONLY in a linked worktree is found" {
  # THE regression test for the first draft's dead-enumeration blocker. Tier R
  # keeps the canonical clean and on primary, so this MUST come from the linked
  # worktree or the emitter reports nothing-to-do while a branch awaits landing.
  _ws_simple a
  _ws_unlanded_default a feat-a
  # canonical really is clean and on primary
  [ "$(command git -C "$WS/a" symbolic-ref --short HEAD)" = "main" ]
  [ -z "$(command git -C "$WS/a" status --porcelain --untracked-files=no)" ]

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '.steps[0].handler')" = "validate" ]
  [ "$(echo "$output" | jq -r '.steps[1].handler')" = "integrate-branch" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/a/.worktrees/feat-a" ]
  [ "$(echo "$output" | jq -r '.steps[1].targetWorktree')" = "$WS/a/.worktrees/feat-a" ]
}

@test "row 2: the main-worktree record is skipped, but a canonical OFF primary is reported" {
  _ws_simple a
  command git -C "$WS/a" checkout -q -b anomaly
  echo more >"$WS/a/more.txt"
  command git -C "$WS/a" add more.txt
  command git -C "$WS/a" commit -q -m "R-3 anomaly"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/a" ]
}

@test "row 2: a stale worktree admin entry is DISCARDED, not reported as detached-head" {
  _ws_simple a
  _ws_unlanded_default a feat-live
  _ws_unlanded "a" "$TEST_DIR/wt-dead" feat-dead
  rm -rf "$TEST_DIR/wt-dead"
  command git -C "$WS/a" worktree list --porcelain | grep -q 'wt-dead'

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/a/.worktrees/feat-live" ]
}

# --- D6: pointed repo wins --------------------------------------------------

@test "D6: --root inside a member is single-repo for THAT repo, siblings ignored" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_unlanded_default a feat-a
  _ws_unlanded_default b feat-b
  WS_EDGES='[{"consumer":"b","alias":"a","target":"a"}]'
  _ws_write_info a b
  _ws_write_lock a b

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '[.steps[].targetWorktree] | unique | .[0]')" = "$WS/a/.worktrees/feat-a" ]
  # the sibling's unlanded work is nowhere in the plan, and its edge with `a`
  # did not turn this into refuse:edges-present
  [[ $output != *"feat-b"* ]]
}

@test "D6: --root INSIDE a work area targets that work area (deepest match wins)" {
  _ws_simple a
  _ws_unlanded_default a feat-a
  mkdir -p "$WS/a/.worktrees/feat-a/deep/deeper"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a/.worktrees/feat-a/deep/deeper"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  # the WORK AREA, never the subdirectory the caller pointed at
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/a/.worktrees/feat-a" ]
}

@test "D6: a worktree OUTSIDE its member canonical still resolves to that member" {
  # Containment is tested against the member's linked worktrees too, not just
  # its canonical path — a coordinated set's member checkout lives outside.
  _ws_simple a
  mkdir -p "$WS/detached-wts"
  _ws_unlanded "a" "$WS/detached-wts/feat-a" feat-a

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/detached-wts/feat-a"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/detached-wts/feat-a" ]
}

@test "D6: pointing at a member with nothing to land reports nothing-to-do at single-repo" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_unlanded_default b feat-b
  _ws_write_info a b
  _ws_write_lock a b

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
}

# --- §7 rows 4 and 5: multi-repo -------------------------------------------

@test "row 4: two touched members with a DIRECT edge refuse with edges-present" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_unlanded_default a feat-a
  _ws_unlanded_default b feat-b
  WS_EDGES='[{"consumer":"b","alias":"a","target":"a"}]'
  _ws_write_info a b
  _ws_write_lock a b

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "refuse" ]
  [ "$(echo "$output" | jq -r '.shape')" = "multi-repo" ]
  [ "$(echo "$output" | jq -r '.reason')" = "edges-present" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
  # the refusal must explain the remedy, and must LEAD with it (see the
  # cap test below)
  [[ $(echo "$output" | jq -r '.display') == "refuse: land these as ONE coordinated set"* ]]
  [[ $(echo "$output" | jq -r '.display') == *"b -> a"* ]]
}

@test "row 5: two touched members with NO direct edge plan order-free at multi-repo" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_member c
  _ws_unlanded_default a feat-a
  _ws_unlanded_default b feat-b
  # c consumes both, but c is UNTOUCHED, so a and b are disjoint (direct edges
  # only — no transitive closure).
  WS_EDGES='[{"consumer":"c","alias":"a","target":"a"},{"consumer":"c","alias":"b","target":"b"}]'
  _ws_write_info a b c
  _ws_write_lock a b c

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "multi-repo" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "4" ]
  [ "$(echo "$output" | jq -r '[.steps[].handler] | join(",")')" = "validate,integrate-branch,validate,integrate-branch" ]
  [ "$(echo "$output" | jq -r '[.steps[].targetWorktree] | unique | sort | join(",")')" = "$WS/a/.worktrees/feat-a,$WS/b/.worktrees/feat-b" ]
}

# --- §7 row 6 / D7: the ambiguity refusal ----------------------------------

@test "row 6 / D7: a repo with TWO unlanded work areas refuses with ambiguous-target" {
  # The second defect the reviews caught: without §5.4's reduction this routed
  # to a "disjoint multi-repo" plan that ff-merged two DIFFERENT branches onto
  # one main and declared them order-free. D7 refuses instead.
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "refuse" ]
  [ "$(echo "$output" | jq -r '.reason')" = "ambiguous-target" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
  # §5.4 step 3: display MUST name the repo and its competing work areas — by
  # repo NAME plus repo-relative area paths, with no absolute path, so this
  # message fits §6.3's 256-character cap no matter how deep the tree is. A
  # display that embedded an absolute path passed in a shallow fixture and lost
  # both area names under a deep one; asserting the ABSENCE of the fixture root
  # is what keeps that from creeping back.
  local display
  display=$(echo "$output" | jq -r '.display')
  [[ $display == *"'a'"* ]]
  [[ $display == *".worktrees/feat-one"* ]]
  [[ $display == *".worktrees/feat-two"* ]]
  [[ $display != *"$TEST_DIR"* ]]
  [ "${#display}" -le 256 ]
}

@test "refusals LEAD with the remedy, so §6.3's 256-char cap can never truncate it" {
  # Found against the LIVE workspace, not by these fixtures: real work-area
  # paths overflow §6.3's cap, so a remedy placed at the END is cut off mid-word
  # — while §7 requires every refusal to explain its remedy. Branch names long
  # enough to overflow the cap on their own reproduce that here.
  local long="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  _ws_simple a
  _ws_unlanded_default a "feat-one-$long"
  _ws_unlanded_default a "feat-two-$long"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.reason')" = "ambiguous-target" ]
  local display
  display=$(echo "$output" | jq -r '.display')
  # the cap holds ...
  [ "${#display}" -le 256 ]
  # ... it really did bite (the second area name did not survive) ...
  [[ $display != *"feat-two-$long"* ]]
  # ... and the remedy did.
  [[ $display == "refuse: re-point --root at the intended work area"* ]]
}

@test "refusal display length does not depend on how deep the tree is" {
  # The invariant that keeps an absolute path from creeping back into an
  # ambiguity refusal: the message must be identical whether the workspace sits
  # one directory down or many. Two fixtures, same repo/branch names, different
  # depth — the displays must match EXACTLY.
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  local shallow
  shallow=$(echo "$output" | jq -r '.display')

  WS="$TEST_DIR/one/two/three/four/five/six/seven/eight/nine/ten/deeply/nested/ws"
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.reason')" = "ambiguous-target" ]
  [ "$(echo "$output" | jq -r '.display')" = "$shallow" ]
}

@test "row 6 / D7: the ambiguity refusal also fires on the pointed-repo path" {
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "refuse" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.reason')" = "ambiguous-target" ]
}

@test "D7's remedy works: pointing --root at one of the competing work areas plans it" {
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a/.worktrees/feat-two"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$WS/a/.worktrees/feat-two" ]
}

@test "D7: one ambiguous repo refuses the whole multi-repo plan, never a per-area step" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two
  _ws_unlanded_default b feat-b
  _ws_write_info a b
  _ws_write_lock a b

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "refuse" ]
  [ "$(echo "$output" | jq -r '.reason')" = "ambiguous-target" ]
  [ "$(echo "$output" | jq -r '.shape')" = "multi-repo" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
}

# --- §7 row 3: the coordinated set -----------------------------------------

@test "row 3: --set-branch plans validate-workforest then land-workforest at the SET dir" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_write_info a b
  _ws_write_lock a b
  local setdir
  setdir=$(_ws_set_dir wf-1)
  _ws_set_member wf-1 a
  _ws_set_member wf-1 b
  MOCK_PNWF_LAND_PLAN_OUTPUT=$(printf 'a\nb')
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '.steps[0].handler')" = "validate-workforest" ]
  [ "$(echo "$output" | jq -r '.steps[1].handler')" = "land-workforest" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$setdir" ]
  [ "$(echo "$output" | jq -r '.steps[1].targetWorktree')" = "$setdir" ]
}

@test "row 3: a set whose members are all landed reports nothing-to-do at shape set" {
  _ws_simple a
  _ws_set_dir wf-1 >/dev/null
  MOCK_PNWF_LAND_PLAN_OUTPUT=""
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
}

@test "D6 override: --set-branch with --root inside a member still yields shape set" {
  _ws_simple a
  _ws_unlanded_default a feat-a
  local setdir
  setdir=$(_ws_set_dir wf-1)
  _ws_set_member wf-1 a
  MOCK_PNWF_LAND_PLAN_OUTPUT="a"
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/a" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$setdir" ]
}

@test "set path: member enumeration is delegated to pnwf with cwd pinned to --root" {
  _ws_simple a
  _ws_set_dir wf-1 >/dev/null
  MOCK_PNWF_LAND_PLAN_OUTPUT=""
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  # `pnwf` resolves the workspace from cwd ALONE, so an unpinned cwd would
  # resolve a different workspace (or none).
  [ "$(cat "$MOCK_PNWF_CWD_LOG")" = "$WS" ]
}

@test "set path: an absent member directory is skipped as already landed" {
  _ws_init
  _ws_member a
  _ws_member b
  _ws_write_info a b
  _ws_write_lock a b
  _ws_set_dir wf-1 >/dev/null
  _ws_set_member wf-1 a
  # b has no checkout in the set: FF-4 already removed it
  MOCK_PNWF_LAND_PLAN_OUTPUT=$(printf 'a\nb')
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
}

# --- §8: detached HEAD halts -----------------------------------------------

@test "detached HEAD on the single-repo path stops with detached-head" {
  _ws_simple a
  command git -C "$WS/a" worktree add -q --detach "$WS/a/.worktrees/loose"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "detached-head" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
  [[ $(echo "$output" | jq -r '.display') == *"'a'"* ]]
}

@test "detached HEAD halts rather than being skipped as a non-target" {
  # A detached work area alongside an unlanded one must NOT quietly yield a
  # plan for the other; correction #9 makes it a halt.
  _ws_simple a
  _ws_unlanded_default a feat-a
  command git -C "$WS/a" worktree add -q --detach "$WS/a/.worktrees/loose"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "detached-head" ]
}

@test "detached HEAD on the SET path stops with detached-head (the emitter's own sweep)" {
  # `pnwf land-plan` never inspects HEAD, so this sweep is wsplan's own duty.
  _ws_simple a
  _ws_set_dir wf-1 >/dev/null
  _ws_set_member wf-1 a
  command git -C "$WS/.workforests/wf-1/a" checkout -q --detach
  MOCK_PNWF_LAND_PLAN_OUTPUT="a"
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.reason')" = "detached-head" ]
}

# --- §5.5: the three ancestry answers --------------------------------------

@test "unborn branch (a repo with no commits) reports nothing-to-do, NOT absent-ref" {
  mkdir -p "$TEST_DIR/unborn"
  command git -C "$TEST_DIR/unborn" init -q -b wip

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/unborn"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
}

@test "an unresolvable ref stops with absent-ref, naming the repo" {
  # primary resolves to `main`, but this repo's only branch is `trunk`, so the
  # ancestry comparison cannot resolve — §5.5's third answer.
  _ws_init
  _ws_member t trunk
  _ws_unlanded_default t feat-t
  _ws_write_info t
  _ws_write_lock t

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "absent-ref" ]
  [[ $(echo "$output" | jq -r '.display') == *"'t'"* ]]
}

# --- §6.1: the remaining stop conditions -----------------------------------

@test "a missing lock stops with missing-lock at shape workspace" {
  _ws_simple a
  rm -f "$WS/pn-workspace.lock.json"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "workspace" ]
  [ "$(echo "$output" | jq -r '.reason')" = "missing-lock" ]
}

@test "an unparseable lock also stops with missing-lock" {
  _ws_simple a
  echo 'not json {' >"$WS/pn-workspace.lock.json"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.reason')" = "missing-lock" ]
}

@test "a member named in the lock with no clone on disk stops with incomplete-workspace" {
  # At the workspace ROOT an absent member clone means an incompletely cloned
  # workspace, NOT landed work — the opposite of the set path.
  _ws_init
  _ws_member a
  _ws_write_info a
  _ws_write_lock a ghost

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "workspace" ]
  [ "$(echo "$output" | jq -r '.reason')" = "incomplete-workspace" ]
  [[ $(echo "$output" | jq -r '.display') == *"ghost"* ]]
}

@test "--root inside a set without --set-branch stops with set-branch-required" {
  # The set dir carries its OWN pn-workspace.toml, so without §5.2's Q2B test
  # this would be silently treated as a workspace root.
  _ws_simple a
  local setdir
  setdir=$(_ws_set_dir wf-1)

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$setdir"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "null" ]
  [ "$(echo "$output" | jq -r '.reason')" = "set-branch-required" ]
}

@test "an absolute workforests_dir stops with unsupported-layout" {
  # `pn` permits it, but pnwf's derivation is unconditionally
  # canonical_root/workforests_dir/branch — so refusing honestly beats emitting
  # steps that cannot execute.
  WS_WORKFORESTS_DIR="/abs/sets"
  export WS_WORKFORESTS_DIR
  _ws_simple a

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.reason')" = "unsupported-layout" ]
}

@test "a non-zero pnwf delegate stops with delegate-failed and carries its diagnostic" {
  _ws_simple a
  _ws_set_dir wf-1 >/dev/null
  MOCK_PNWF_LAND_PLAN_RC=3
  MOCK_PNWF_LAND_PLAN_OUTPUT="pnwf: error: lock file not found"
  export MOCK_PNWF_LAND_PLAN_RC MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.reason')" = "delegate-failed" ]
  [[ $(echo "$output" | jq -r '.display') == *"lock file not found"* ]]
}

@test "unusable pnwf output stops with delegate-failed rather than being parsed as members" {
  _ws_simple a
  _ws_set_dir wf-1 >/dev/null
  MOCK_PNWF_LAND_PLAN_OUTPUT="warning: something odd happened"
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "delegate-failed" ]
}

@test "a failing integrate-branch-support stops with delegate-failed" {
  _ws_simple a
  _ws_unlanded_default a feat-a
  MOCK_IBS_RC=7
  export MOCK_IBS_RC

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "delegate-failed" ]
  [[ $(echo "$output" | jq -r '.display') == *"integrate-branch-support"* ]]
}

@test "--root outside any git repo and any workspace stops with not-a-repo and shape null" {
  mkdir -p "$TEST_DIR/plain"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/plain"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.shape')" = "null" ]
  [ "$(echo "$output" | jq -r '.reason')" = "not-a-repo" ]
}

@test "a charset-violating work-area path stops with bad-path and withholds the path" {
  _ws_simple a
  mkdir -p "$TEST_DIR/has space"
  _ws_unlanded "a" "$TEST_DIR/has space/wt" feat-space

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
  [ "$(echo "$output" | jq -r '.reason')" = "bad-path" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
  # the path itself is deliberately withheld from the model-facing display
  [[ $(echo "$output" | jq -r '.display') != *"has space"* ]]
  [[ $(echo "$output" | jq -r '.display') == *"'a'"* ]]
}

# --- the standalone (non-workspace) path -----------------------------------

@test "a standalone repo outside any workspace plans at single-repo" {
  mkdir -p "$TEST_DIR/solo"
  command git -C "$TEST_DIR/solo" init -q -b main
  command git -C "$TEST_DIR/solo" config user.email "test@example.com"
  command git -C "$TEST_DIR/solo" config user.name "Test"
  echo one >"$TEST_DIR/solo/f.txt"
  command git -C "$TEST_DIR/solo" add f.txt
  command git -C "$TEST_DIR/solo" commit -q -m initial
  command git -C "$TEST_DIR/solo" worktree add -q "$TEST_DIR/solo/.worktrees/feat" -b feat
  echo work >"$TEST_DIR/solo/.worktrees/feat/w.txt"
  command git -C "$TEST_DIR/solo/.worktrees/feat" add w.txt
  command git -C "$TEST_DIR/solo/.worktrees/feat" commit -q -m work

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/solo"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$TEST_DIR/solo/.worktrees/feat" ]
}

@test "a DEEP --root in a standalone repo emits the work area, never the subdirectory" {
  mkdir -p "$TEST_DIR/solo"
  command git -C "$TEST_DIR/solo" init -q -b main
  command git -C "$TEST_DIR/solo" config user.email "test@example.com"
  command git -C "$TEST_DIR/solo" config user.name "Test"
  mkdir -p "$TEST_DIR/solo/sub/deeper"
  echo one >"$TEST_DIR/solo/sub/deeper/f.txt"
  command git -C "$TEST_DIR/solo" add .
  command git -C "$TEST_DIR/solo" commit -q -m initial
  command git -C "$TEST_DIR/solo" checkout -q -b feat
  echo work >"$TEST_DIR/solo/w.txt"
  command git -C "$TEST_DIR/solo" add w.txt
  command git -C "$TEST_DIR/solo" commit -q -m work

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/solo/sub/deeper"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$TEST_DIR/solo" ]
}

# --- §4: the cwd rule ------------------------------------------------------

@test "cwd is irrelevant: running from HOME (outside any workspace) is byte-identical" {
  _ws_simple a
  _ws_unlanded_default a feat-a

  cd "$WS"
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  local from_ws="$output"

  cd "$HOME"
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$output" = "$from_ws" ]
}

@test "cwd is irrelevant on the set path too" {
  _ws_simple a
  local setdir
  setdir=$(_ws_set_dir wf-1)
  _ws_set_member wf-1 a
  MOCK_PNWF_LAND_PLAN_OUTPUT="a"
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  cd "$HOME"
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$setdir" ]
  # every delegate ran with cwd pinned to --root, not to HOME
  [ "$(cat "$MOCK_PNWF_CWD_LOG")" = "$WS" ]
}

@test "an inherited PN_WORKSPACE_ROOT cannot redirect the resolution away from --root" {
  # `pn` honors an exported PN_WORKSPACE_ROOT BEFORE its cwd walk, so a stale
  # value would silently defeat the cwd pinning that IS this tool's determinism
  # guarantee. wsplan clears it (env -u), so a bogus value must change nothing.
  _ws_simple a
  _ws_unlanded_default a feat-a

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  local clean="$output"

  PN_WORKSPACE_ROOT="$TEST_DIR/nowhere"
  export PN_WORKSPACE_ROOT
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$output" = "$clean" ]
}

# --- §6.2: the exit-code contract ------------------------------------------

@test "exit codes: refuse and stopped both exit 0 WITH an envelope" {
  _ws_simple a
  _ws_unlanded_default a feat-one
  _ws_unlanded_default a feat-two
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "refuse" ]

  mkdir -p "$TEST_DIR/plain"
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/plain"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "stopped" ]
}

@test "stdout is EXACTLY one JSON object and parses under jq -e" {
  _ws_simple a
  _ws_unlanded_default a feat-a
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e . >/dev/null
  [ "$(echo "$output" | jq -s 'length')" = "1" ]
  [ "$(echo "$output" | jq -c 'keys_unsorted')" = '["version","outcome","shape","reason","steps","display"]' ]
}

@test "usage: the three §4 precondition failures behave identically with and without --set-branch" {
  # §4's preconditions are checked BEFORE shape routing, so identical malformed
  # input must yield the identical usage error either way: non-zero, and NO
  # envelope on stdout.
  local args
  for args in "" "--set-branch wf-1"; do
    # (1) --root missing
    # shellcheck disable=SC2086  # deliberate splitting of the flag pair under test
    run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan $args
    [ "$status" -ne 0 ]
    [ -z "$output" ]
    [[ $stderr == *"--root is required"* ]]

    # (2) --root relative
    # shellcheck disable=SC2086  # deliberate splitting of the flag pair under test
    run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root relative/path $args
    [ "$status" -ne 0 ]
    [ -z "$output" ]
    [[ $stderr == *"absolute path"* ]]

    # (3) --root nonexistent
    # shellcheck disable=SC2086  # deliberate splitting of the flag pair under test
    run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR/no-such-dir" $args
    [ "$status" -ne 0 ]
    [ -z "$output" ]
    [[ $stderr == *"existing directory"* ]]
  done
}

@test "usage: --root inside the workspace but neither the root nor a member is a usage error" {
  _ws_simple a
  mkdir -p "$WS/not-a-member"

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/not-a-member"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
  [[ $stderr == *"neither the workspace root nor inside any member repo"* ]]
}

@test "usage: that same --root DOES yield a set envelope when --set-branch is given" {
  # §5.2 tests Q1 first, and `pn workspace info --json` is cwd-stable anywhere
  # in the workspace, so the set directory resolves fine from such a --root.
  # This is deliberately NOT a usage error.
  _ws_simple a
  mkdir -p "$WS/not-a-member"
  local setdir
  setdir=$(_ws_set_dir wf-1)
  _ws_set_member wf-1 a
  MOCK_PNWF_LAND_PLAN_OUTPUT="a"
  export MOCK_PNWF_LAND_PLAN_OUTPUT

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$WS/not-a-member" --set-branch wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "$setdir" ]
}

@test "usage: an unknown flag, an unknown subcommand and a stray positional all fail" {
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR" --nope
  [ "$status" -ne 0 ]
  [ -z "$output" ]

  run --separate-stderr "$SCRIPT_UNDER_TEST" frobnicate
  [ "$status" -ne 0 ]
  [ -z "$output" ]

  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root "$TEST_DIR" extra
  [ "$status" -ne 0 ]
  [ -z "$output" ]

  run --separate-stderr "$SCRIPT_UNDER_TEST" --bogus
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "usage: --root and --set-branch accept both the space and the = form" {
  _ws_simple a
  _ws_unlanded_default a feat-a
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root="$WS"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]

  _ws_set_dir wf-1 >/dev/null
  MOCK_PNWF_LAND_PLAN_OUTPUT=""
  export MOCK_PNWF_LAND_PLAN_OUTPUT
  run --separate-stderr "$SCRIPT_UNDER_TEST" land-plan --root="$WS" --set-branch=wf-1
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.shape')" = "set" ]
}

@test "help: --help exits 0 and documents the subcommand, both flags and the enums" {
  run --separate-stderr "$SCRIPT_UNDER_TEST" --help
  [ "$status" -eq 0 ]
  [[ $output == *"land-plan"* ]]
  [[ $output == *"--root"* ]]
  [[ $output == *"--set-branch"* ]]
  [[ $output == *"nothing-to-do"* ]]
  [[ $output == *"Usage:"* ]]
}

@test "help: no arguments prints help and exits non-zero" {
  run --separate-stderr "$SCRIPT_UNDER_TEST"
  [ "$status" -ne 0 ]
  [[ $output == *"Usage:"* ]]
}
