#!/usr/bin/env bats

bats_require_minimum_version 1.5.0

# Unit suite for wsplan.bash: the detection, reduction, edge-test and
# envelope-construction primitives, exercised WITHOUT going through argument
# parsing. That separation is the reason the design mandates the .sh/.bash
# split (§3) — the reduction rule and the edge test are where the two silent
# defects the reviews caught live, and both are pure functions over inputs.
#
# Two harness shapes, deliberately:
#
#  * Most tests source pnwf-lib.bash then wsplan.bash into bats' own shell (the
#    SAME order the builder composes them) and call the function directly. For
#    the envelope/steps/sanitize/charset/reduce builders that is sufficient AND
#    keeps the fixtures readable — they contain no exit-code-as-boolean git
#    probe whose set -e survival could be at issue.
#  * The git-probing functions (wsplan_all_worktrees, wsplan_work_areas,
#    wsplan_classify_work_area) additionally run inside a fresh
#    `bash -euo pipefail -c` subprocess, because bats' own shell is NOT -e and
#    only a real errexit caller can observe a guarded probe aborting. That is
#    the same crux the pnwf-lib suite documents at its head.
#
# The version output is NOT tested: the builder injects it and it does not
# exist in source.

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR

  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pnwf-lib.bash"
  fi
  export LIB_PATH

  # In the nix check LIB_PATH is a colon-separated list of composed library
  # store paths; locally it is the source file (or its directory). Resolve all
  # three shapes to one file, exactly as test-pnwf.bats does.
  local resolved_lib
  if [[ -d ${LIB_PATH} ]]; then
    resolved_lib="${LIB_PATH}/pnwf-lib.bash"
  else
    resolved_lib="${LIB_PATH%%:*}"
  fi
  RESOLVED_LIB="$resolved_lib"
  export RESOLVED_LIB

  # Sourced by every `bash -euo pipefail -c` probe below, in builder order.
  # This is bash SOURCE TEXT for a child shell, not a command to run here, so
  # its quotes are meant to be literal — which is precisely what SC2089/SC2090
  # flag when they assume an array was intended.
  # shellcheck disable=SC2089  # deliberate: `bash -c` source text, not a command
  PRELUDE="source '$RESOLVED_LIB'; source '$SCRIPTS_DIR/wsplan.bash';"
  # shellcheck disable=SC2090  # deliberate: the quotes are for the child shell
  export PRELUDE

  # Hermetic + fast git (same guard as the pnwf suites): a developer's global
  # core.fsmonitor=true makes every throwaway `git init` spawn a daemon that
  # blocks each working-tree op for seconds. GIT_CONFIG_COUNT acts like a `-c`
  # flag, so it wins over the inherited global and is surgical.
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

  MOCK_TEMPLATE="$BATS_FILE_TMPDIR/mock-template"
  mkdir -p "$MOCK_TEMPLATE"
  export MOCK_TEMPLATE
  cat >"$MOCK_TEMPLATE/integrate-branch-support" <<'MOCK'
#!/usr/bin/env bash
echo '{"primary_branch":"main","strategy":null}'
MOCK
  chmod +x "$MOCK_TEMPLATE/integrate-branch-support"
}

setup() {
  # `pwd -P` because git reports PHYSICAL paths (macOS's /var is a symlink to
  # /private/var), and every containment/equality assertion below compares a
  # fixture path against git's own output.
  TEST_DIR="$(cd "$(mktemp -d)" && pwd -P)"
  export TEST_DIR

  # Mocks live OUTSIDE any git working tree a test creates.
  MOCK_BIN="$TEST_DIR/mock-bin"
  mkdir -p "$MOCK_BIN"
  cp -p "$MOCK_TEMPLATE/integrate-branch-support" "$MOCK_BIN/"
  PATH="$MOCK_BIN:$PATH"
  export PATH MOCK_BIN

  HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  export HOME

  # A four-repo edge fixture: b consumes a, c consumes b, d consumes a. So
  # {b,a} has a DIRECT edge, {b,d} has none, and {a,c} is connected only
  # TRANSITIVELY through the untouched b — the case the edge test must call
  # disjoint.
  LOCK="$TEST_DIR/pn-workspace.lock.json"
  cat >"$LOCK" <<'JSON'
{
  "order": ["a", "b", "c", "d"],
  "terminal": "c",
  "repos": {},
  "edges": [
    { "consumer": "b", "alias": "a", "target": "a" },
    { "consumer": "c", "alias": "b", "target": "b" },
    { "consumer": "d", "alias": "a", "target": "a" }
  ]
}
JSON
  export LOCK

  # shellcheck disable=SC1090  # runtime-resolved library path (nix store or source tree)
  source "$RESOLVED_LIB"
  # shellcheck disable=SC1091  # SCRIPTS_DIR is resolved at runtime
  source "$SCRIPTS_DIR/wsplan.bash"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- fixture helpers --------------------------------------------------------

# A real canonical git repo at $TEST_DIR/<name> with one commit on <branch>
# (default main). `command git` bypasses any mock.
_init_repo() {
  local name="$1" branch="${2:-main}"
  local dir="$TEST_DIR/$name"
  mkdir -p "$dir"
  command git -C "$dir" init -q -b "$branch"
  command git -C "$dir" config user.email "test@example.com"
  command git -C "$dir" config user.name "Test"
  echo one >"$dir/file.txt"
  command git -C "$dir" add file.txt
  command git -C "$dir" commit -q -m initial
  printf '%s\n' "$dir"
}

# A linked worktree of <repo_dir> at <path> on a NEW branch <branch>, carrying
# one commit so it is genuinely ahead of (i.e. not landed on) primary.
_add_unlanded_worktree() {
  local repo_dir="$1" path="$2" branch="$3"
  command git -C "$repo_dir" worktree add -q "$path" -b "$branch"
  echo work >"$path/work.txt"
  command git -C "$path" add work.txt
  command git -C "$path" commit -q -m "work on $branch"
}

# --- the edge test (design §5.6) --------------------------------------------

@test "edge test: a direct edge among TOUCHED is reported" {
  run wsplan_direct_edges_among "$LOCK" b a
  [ "$status" -eq 0 ]
  [ "$output" = "b -> a" ]
}

@test "edge test: a disjoint pair reports nothing" {
  run wsplan_direct_edges_among "$LOCK" b d
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "edge test: a TRANSITIVE-only pair is disjoint (no closure is computed)" {
  # c consumes b consumes a. With b UNTOUCHED, {a,c} has no direct edge, so it
  # is disjoint — exactly parent §4.1's rule. A transitive closure here would
  # wrongly refuse a legitimate two-repo plan.
  run wsplan_direct_edges_among "$LOCK" a c
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "edge test: an empty TOUCHED set reports nothing" {
  run wsplan_direct_edges_among "$LOCK"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "edge test: a single touched repo reports nothing even though it has edges" {
  run wsplan_direct_edges_among "$LOCK" b
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "edge test: a lock with no edges key is disjoint, not an error" {
  echo '{"order":["a"],"repos":{}}' >"$TEST_DIR/noedges.json"
  run wsplan_direct_edges_among "$TEST_DIR/noedges.json" a b
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "edge test: an unreadable lock fails, so the caller can report missing-lock" {
  run wsplan_direct_edges_among "$TEST_DIR/nope.json" a b
  [ "$status" -ne 0 ]
}

@test "wsplan_lock_readable: present+parseable true, missing false, unparseable false" {
  run wsplan_lock_readable "$LOCK"
  [ "$status" -eq 0 ]
  run wsplan_lock_readable "$TEST_DIR/nope.json"
  [ "$status" -ne 0 ]
  echo 'not json {' >"$TEST_DIR/bad.json"
  run wsplan_lock_readable "$TEST_DIR/bad.json"
  [ "$status" -ne 0 ]
}

# --- the reduction rule (design §5.4) ---------------------------------------

@test "reduction: one unlanded work area per repo yields count 1 per repo" {
  local input
  input=$(printf 'r1\tnot-landed\t/p/one\nr2\tlanded\t/p/two\nr2\tnot-landed\t/p/three\n')
  local got
  got=$(printf '%s\n' "$input" | wsplan_reduce)
  [ "$got" = "$(printf 'r1\t1\t/p/one\nr2\t1\t/p/three')" ]
}

@test "reduction: TWO unlanded work areas in ONE repo yield count 2 (never a two-step plan)" {
  # THE regression test for the defect that would have ff-merged two different
  # branches onto one primary and declared them order-free. The reduction must
  # surface the plurality as a COUNT on a single repo row, so D7 can refuse —
  # it must never emit two independent rows, and never silently pick one.
  local got
  got=$(printf 'r1\tnot-landed\t/p/a\nr1\tnot-landed\t/p/b\n' | wsplan_reduce)
  [ "$got" = "$(printf 'r1\t2\t/p/a /p/b')" ]
  # exactly ONE row for that repo
  [ "$(printf '%s\n' "$got" | wc -l | tr -d ' ')" = "1" ]
}

@test "reduction: landed and unborn areas contribute nothing to TOUCHED" {
  local got
  got=$(printf 'r1\tlanded\t/p/a\nr2\tunborn\t/p/b\n' | wsplan_reduce)
  [ -z "$got" ]
}

@test "reduction: empty input yields empty output" {
  local got
  got=$(printf '' | wsplan_reduce)
  [ -z "$got" ]
}

@test "reduction: repo order is first-seen, not sorted" {
  local got
  got=$(printf 'zz\tnot-landed\t/p/z\naa\tnot-landed\t/p/a\n' | wsplan_reduce)
  [ "$got" = "$(printf 'zz\t1\t/p/z\naa\t1\t/p/a')" ]
}

# --- envelope construction (design §6) --------------------------------------

@test "envelope: a plan carries version 1, a null reason and its steps" {
  local steps
  steps=$(wsplan_steps_json validate integrate-branch /abs/wt)
  run wsplan_emit plan single-repo "" "1 repo ahead of main" "$steps"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.version')" = "1" ]
  [ "$(echo "$output" | jq -r '.outcome')" = "plan" ]
  [ "$(echo "$output" | jq -r '.shape')" = "single-repo" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
  [ "$(echo "$output" | jq -r '.display')" = "1 repo ahead of main" ]
  [ "$(echo "$output" | jq -r '.steps | length')" = "2" ]
  [ "$(echo "$output" | jq -r '.steps[0].handler')" = "validate" ]
  [ "$(echo "$output" | jq -r '.steps[0].targetWorktree')" = "/abs/wt" ]
  [ "$(echo "$output" | jq -r '.steps[1].handler')" = "integrate-branch" ]
  [ "$(echo "$output" | jq -r '.steps[1].targetWorktree')" = "/abs/wt" ]
}

@test "envelope: nothing-to-do keeps the detected shape, a null reason and empty steps" {
  run wsplan_emit nothing-to-do workspace "" "nothing to land"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.outcome')" = "nothing-to-do" ]
  [ "$(echo "$output" | jq -r '.shape')" = "workspace" ]
  [ "$(echo "$output" | jq -r '.reason')" = "null" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
}

@test "envelope: refuse carries a non-null reason and empty steps" {
  run wsplan_emit refuse multi-repo edges-present "form a set"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.reason')" = "edges-present" ]
  [ "$(echo "$output" | jq -c '.steps')" = "[]" ]
}

@test "envelope: a stopped run that never classified emits shape null" {
  run wsplan_emit stopped "" not-a-repo "outside any repo"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.shape')" = "null" ]
  [ "$(echo "$output" | jq 'has("shape")')" = "true" ]
}

@test "envelope: output is exactly one JSON object with exactly the six §6 fields" {
  run wsplan_emit stopped set detached-head "member x is detached"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -c 'keys_unsorted')" = '["version","outcome","shape","reason","steps","display"]' ]
  [ "$(echo "$output" | jq -s 'length')" = "1" ]
}

@test "envelope: a reason on a plan is refused (§6 invariant)" {
  local steps
  steps=$(wsplan_steps_json validate integrate-branch /abs/wt)
  run wsplan_emit plan single-repo edges-present "wrong" "$steps"
  [ "$status" -ne 0 ]
  [[ $output == *"MUST carry a null reason"* ]]
}

@test "envelope: a missing reason on refuse/stopped is refused (§6 invariant)" {
  run wsplan_emit refuse multi-repo "" "wrong"
  [ "$status" -ne 0 ]
  run wsplan_emit stopped workspace "" "wrong"
  [ "$status" -ne 0 ]
}

@test "envelope: non-empty steps on a non-plan outcome are refused (§6 invariant)" {
  local steps
  steps=$(wsplan_steps_json validate integrate-branch /abs/wt)
  run wsplan_emit refuse multi-repo edges-present "wrong" "$steps"
  [ "$status" -ne 0 ]
  [[ $output == *"MUST carry an empty steps array"* ]]
}

@test "envelope: a plan with no steps is refused (§6 invariant)" {
  run wsplan_emit plan single-repo "" "empty plan" "[]"
  [ "$status" -ne 0 ]
  [[ $output == *"at least one step"* ]]
}

@test "envelope: outcome, shape and reason enum values are enforced" {
  run wsplan_emit maybe single-repo "" "x"
  [ "$status" -ne 0 ]
  [[ $output == *"unknown outcome"* ]]

  run wsplan_emit stopped monorepo not-a-repo "x"
  [ "$status" -ne 0 ]
  [[ $output == *"unknown shape"* ]]

  run wsplan_emit stopped single-repo everything-broke "x"
  [ "$status" -ne 0 ]
  [[ $output == *"unknown reason"* ]]
}

@test "envelope: every §6/§6.1 enum member is accepted" {
  local s r
  for s in single-repo set multi-repo workspace; do
    run wsplan_emit nothing-to-do "$s" "" "ok"
    [ "$status" -eq 0 ]
    [ "$(echo "$output" | jq -r '.shape')" = "$s" ]
  done
  for r in edges-present ambiguous-target detached-head absent-ref bad-path \
    missing-lock not-a-repo set-branch-required incomplete-workspace \
    unsupported-layout delegate-failed; do
    run wsplan_emit stopped "" "$r" "ok"
    [ "$status" -eq 0 ]
    [ "$(echo "$output" | jq -r '.reason')" = "$r" ]
  done
  # `plan` and `refuse`, the two outcomes with their own field invariants, are
  # covered by the dedicated tests above; asserted here only for completeness of
  # the outcome enum itself.
  run wsplan_emit refuse multi-repo edges-present "ok"
  [ "$status" -eq 0 ]
  run wsplan_emit plan set "" "ok" "$(wsplan_steps_json validate-workforest land-workforest /abs/set)"
  [ "$status" -eq 0 ]
}

# --- steps (design §6) -----------------------------------------------------

@test "steps: the four handler names are accepted and no others" {
  run wsplan_steps_json validate integrate-branch /abs/a
  [ "$status" -eq 0 ]
  run wsplan_steps_json validate-workforest land-workforest /abs/set
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.[0].handler')" = "validate-workforest" ]
  [ "$(echo "$output" | jq -r '.[1].handler')" = "land-workforest" ]

  run wsplan_steps_json cleanup integrate-branch /abs/a
  [ "$status" -ne 0 ]
  [[ $output == *"unknown handler: cleanup"* ]]
}

@test "steps: several work areas each contribute an ordered handler pair" {
  run wsplan_steps_json validate integrate-branch /abs/a /abs/b
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r 'length')" = "4" ]
  [ "$(echo "$output" | jq -r '[.[].targetWorktree] | join(",")')" = "/abs/a,/abs/a,/abs/b,/abs/b" ]
  [ "$(echo "$output" | jq -r '[.[].handler] | join(",")')" = "validate,integrate-branch,validate,integrate-branch" ]
}

@test "steps: no work areas yields an empty array" {
  run wsplan_steps_json validate integrate-branch
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "steps: handlers take no arguments — a step has exactly handler+targetWorktree" {
  run wsplan_steps_json validate integrate-branch /abs/a
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -c '.[0] | keys_unsorted')" = '["handler","targetWorktree"]' ]
}

# --- the trust boundary (design §6.3) --------------------------------------

@test "display sanitization: control characters are stripped" {
  local got
  # backspace, SOH, STX and DEL all vanish outright
  got=$(wsplan_sanitize_display "$(printf 'a\bb\001c\002d\177e')")
  [ "$got" = "abcde" ]
}

@test "display sanitization: vertical tab and form feed fold to spaces, like newlines" {
  # They are LAYOUT control characters, so folding them to a separator rather
  # than deleting them keeps two words from silently fusing into one.
  local got
  got=$(wsplan_sanitize_display "$(printf 'one\vtwo\fthree')")
  [ "$got" = "one two three" ]
}

@test "display sanitization: the ESC byte of an ANSI sequence is stripped" {
  # Scope note, asserted rather than assumed: §6.3 mandates removing CONTROL
  # CHARACTERS, so the ESC (\033) goes but its PRINTABLE parameter bytes stay.
  # The threat model is instruction-shaped text reaching a model, not terminal
  # rendering, so the surviving "[31m" is inert and in scope for `display`.
  local got
  got=$(wsplan_sanitize_display "$(printf 'a\033[31mb')")
  [ "$got" = "a[31mb" ]
  [[ $got != *$'\033'* ]]
}

@test "display sanitization: newlines collapse to single spaces" {
  local got
  got=$(wsplan_sanitize_display "$(printf 'one\ntwo\r\nthree')")
  [ "$got" = "one two three" ]
}

@test "display sanitization: length is capped at 256 characters" {
  local long got
  long=$(printf 'x%.0s' $(seq 1 400))
  got=$(wsplan_sanitize_display "$long")
  [ "${#got}" -eq 256 ]
}

@test "display sanitization: a sanitized display is what the envelope carries" {
  run wsplan_emit nothing-to-do workspace "" "$(printf 'line1\nline2')"
  [ "$status" -eq 0 ]
  [ "$(echo "$output" | jq -r '.display')" = "line1 line2" ]
}

@test "relativize: a path under the base loses the prefix, the base itself becomes '.'" {
  # The display-side rendering §6.3's 256-character cap forces: two REAL absolute
  # work-area paths overflow it, so an ambiguity refusal names the repo canonical
  # once and each area relative to it. `targetWorktree` is never relativized.
  run wsplan_relativize /a/b /a/b/.worktrees/feat
  [ "$status" -eq 0 ]
  [ "$output" = ".worktrees/feat" ]

  run wsplan_relativize /a/b /a/b
  [ "$status" -eq 0 ]
  [ "$output" = "." ]
}

@test "relativize: a path OUTSIDE the base is left absolute, and a sibling prefix is not stripped" {
  run wsplan_relativize /a/b /elsewhere/wt
  [ "$status" -eq 0 ]
  [ "$output" = "/elsewhere/wt" ]

  # /a/bb must NOT match the /a/b prefix — the check is on a path-segment boundary
  run wsplan_relativize /a/b /a/bb/wt
  [ "$status" -eq 0 ]
  [ "$output" = "/a/bb/wt" ]
}

@test "path charset: command substitution, a semicolon and a space are all refused" {
  run wsplan_path_ok '/tmp/x$(id)'
  [ "$status" -ne 0 ]
  run wsplan_path_ok '/tmp/a;b&c'
  [ "$status" -ne 0 ]
  run wsplan_path_ok '/tmp/has space/wt'
  [ "$status" -ne 0 ]
  run wsplan_path_ok '/tmp/back`tick`'
  [ "$status" -ne 0 ]
  run wsplan_path_ok '/tmp/tilde~x'
  [ "$status" -ne 0 ]
}

@test "path charset: real emitter paths are accepted" {
  run wsplan_path_ok "$TEST_DIR"
  [ "$status" -eq 0 ]
  run wsplan_path_ok /Users/me/phillipg_mbp/phillipg-nix-repo-base/.worktrees/pg2-wjt8k
  [ "$status" -eq 0 ]
  run wsplan_path_ok /Users/me/ws/.workforests/wf/pg2-abc-slug
  [ "$status" -eq 0 ]
  run wsplan_path_ok /Users/me+extra/repo@v1/wt-1.2
  [ "$status" -eq 0 ]
}

@test "path charset: a charset-violating path is refused BEFORE it can reach a step" {
  run wsplan_steps_json validate integrate-branch '/tmp/x$(id)'
  [ "$status" -ne 0 ]
  [[ $output == *"charset check"* ]]
}

# --- work-area enumeration (design §5.1) -----------------------------------

@test "work areas: a linked worktree is found while the canonical sits clean on primary" {
  # The primary defect of the first draft: Tier R keeps canonical clones clean
  # and on primary, so a canonical-only walk finds nothing at all.
  local repo
  repo=$(_init_repo repoA)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-a" feat-a

  run bash -euo pipefail -c "$PRELUDE wsplan_work_areas '$repo' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/wt-a" ]
}

@test "work areas: the main-worktree record is skipped when the canonical is on primary" {
  local repo
  repo=$(_init_repo repoA)
  run bash -euo pipefail -c "$PRELUDE wsplan_work_areas '$repo' main"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "work areas: a canonical OFF its primary is reported by item 2" {
  local repo
  repo=$(_init_repo repoA)
  command git -C "$repo" checkout -q -b anomaly
  run bash -euo pipefail -c "$PRELUDE wsplan_work_areas '$repo' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$repo" ]
}

@test "work areas: a stale (prunable) admin entry is DISCARDED, never reported" {
  # .git/worktrees admin entries linger until an explicit `git worktree prune`.
  # Reporting one would fail the §8 symbolic-ref probe and halt the land as
  # detached-head for a directory that does not exist. wsplan is read-only, so
  # it discards rather than prunes.
  local repo
  repo=$(_init_repo repoA)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-live" feat-live
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-dead" feat-dead
  rm -rf "$TEST_DIR/wt-dead"
  # git still lists it
  command git -C "$repo" worktree list --porcelain | grep -q 'wt-dead'

  run bash -euo pipefail -c "$PRELUDE wsplan_work_areas '$repo' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/wt-live" ]
}

@test "work areas: every linked worktree is reported, canonical last when off primary" {
  local repo
  repo=$(_init_repo repoA)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-1" feat-1
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-2" feat-2
  command git -C "$repo" checkout -q -b anomaly

  run bash -euo pipefail -c "$PRELUDE wsplan_work_areas '$repo' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf '%s\n%s\n%s' "$TEST_DIR/wt-1" "$TEST_DIR/wt-2" "$repo")" ]
}

@test "wsplan_all_worktrees: the main worktree is FIRST, even when run from a linked one" {
  local repo
  repo=$(_init_repo repoA)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-a" feat-a

  run bash -euo pipefail -c "$PRELUDE wsplan_all_worktrees '$TEST_DIR/wt-a'"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | head -1)" = "$repo" ]
}

@test "wsplan_all_worktrees: a non-repo directory fails without aborting a set -e caller" {
  mkdir -p "$TEST_DIR/plain"
  run bash -euo pipefail -c "$PRELUDE rc=0; wsplan_all_worktrees '$TEST_DIR/plain' || rc=\$?; echo \"rc=\$rc\""
  [ "$status" -eq 0 ]
  [[ $output == *"rc="* ]]
  [[ $output != *"rc=0"* ]]
  [[ $output == *"git worktree list failed"* ]]
}

# --- classification (design §5.5, §8) --------------------------------------

@test "classify: an unlanded branch is not-landed and names its branch" {
  local repo
  repo=$(_init_repo repoA)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-a" feat-a

  run bash -euo pipefail -c "$PRELUDE wsplan_classify_work_area '$TEST_DIR/wt-a' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'not-landed\tfeat-a')" ]
}

@test "classify: a branch that is an ancestor of primary is landed" {
  local repo
  repo=$(_init_repo repoA)
  command git -C "$repo" worktree add -q "$TEST_DIR/wt-a" -b feat-a
  run bash -euo pipefail -c "$PRELUDE wsplan_classify_work_area '$TEST_DIR/wt-a' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'landed\tfeat-a')" ]
}

@test "classify: a detached HEAD is reported detached, and is never compared" {
  local repo
  repo=$(_init_repo repoA)
  command git -C "$repo" worktree add -q --detach "$TEST_DIR/wt-d"
  run bash -euo pipefail -c "$PRELUDE wsplan_classify_work_area '$TEST_DIR/wt-d' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'detached\t')" ]
}

@test "classify: an unborn HEAD is unborn, NOT absent (§5.5)" {
  # symbolic-ref SUCCEEDS on an unborn branch (HEAD is a symref to an
  # uncreated ref), so the detached test correctly passes — but the ancestry
  # check would answer `absent`. An empty repo has nothing to land, so this
  # must classify as unborn and route to nothing-to-do.
  mkdir -p "$TEST_DIR/unborn"
  command git -C "$TEST_DIR/unborn" init -q -b wip
  run bash -euo pipefail -c "$PRELUDE wsplan_classify_work_area '$TEST_DIR/unborn' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'unborn\twip')" ]
}

@test "classify: an unresolvable primary is absent, and does not abort a set -e caller" {
  local repo
  repo=$(_init_repo repoT trunk)
  _add_unlanded_worktree "$repo" "$TEST_DIR/wt-a" feat-a
  # primary 'main' does not exist in this repo
  run bash -euo pipefail -c "$PRELUDE wsplan_classify_work_area '$TEST_DIR/wt-a' main"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'absent\tfeat-a')" ]
}
