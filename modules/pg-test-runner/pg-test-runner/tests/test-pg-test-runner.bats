#!/usr/bin/env bats
# CLI/integration suite for pg-test-runner, driving the ASSEMBLED artifact via
# SCRIPT_UNDER_TEST (bead pg2-28wwb convention): in the nix check that is the
# real wrapped binary; for a local `bats tests/` run (no nix build) setup()
# below assembles an equivalent wrapper sourcing pg-test-runner.bash then
# pg-test-runner.sh in the SAME order the builder composes them (the "support
# .bash" convention -- mkBashScript sources it before the .sh body -- no
# separate mkBashLibrary is involved here, unlike pg-go-mutate/pnwf).
#
# Every project fixture uses only `go` and `bats` (both real requirements of
# this suite's own check via testDeps) so these tests run identically inside
# the nix sandbox and on a developer machine with no python/npm available.
#
# Covers the section 2.3 discovery edge cases the design reviews flagged:
# root-marker repos, fixture pruning, dual-marker directories, file args vs
# directory args, a nonexistent path, and CLI mode exclusivity.

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
}

setup() {
  if [[ -n "${SCRIPT_UNDER_TEST:-}" ]]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    TEST_WRAPPER_DIR="$(mktemp -d)"
    cat >"$TEST_WRAPPER_DIR/pg-test-runner-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$SCRIPTS_DIR/pg-test-runner.bash"
source "$SCRIPTS_DIR/pg-test-runner.sh"
WRAPPER
    chmod +x "$TEST_WRAPPER_DIR/pg-test-runner-wrapper"
    SCRIPT="$TEST_WRAPPER_DIR/pg-test-runner-wrapper"
  fi
  export SCRIPT

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # Hermetic against the caller's environment (bead pg2-27c6c): every
  # PG_TEST_RUNNER_* knob the runner reads that these tests assume unset. The
  # tests that exercise a knob set it explicitly. PG_TEST_RUNNER_JQ_BIN and
  # PG_TEST_RUNNER_TIMEOUT_BIN are deliberately kept: they only point at tool
  # binaries and are not behaviour knobs.
  unset PG_TEST_RUNNER_CONFIG PG_TEST_RUNNER_DEFAULT_CONFIG \
    PG_TEST_RUNNER_PROJECT_CONCURRENCY PG_TEST_RUNNER_MAX_CONCURRENT_RUNS \
    PG_TEST_RUNNER_NICE_LEVEL PG_TEST_RUNNER_SLOT_WAIT_SECONDS \
    PG_TEST_RUNNER_LOCK_ROOT PG_TEST_RUNNER_TMP_ROOT

  # A minimal, deterministic config using only go + bats (both available to
  # this check via testDeps) -- see pg-test-runner/default.nix. timeoutSeconds
  # matches the production default (modules/pg-test-runner/config.nix) rather
  # than an independent, tighter value: a subprocess this suite spawns (e.g.
  # a fixture's `go test`) can genuinely take longer than a short fixed cap
  # under this workspace's normal concurrent-session load, timing out a
  # healthy run rather than catching a real bug (pg2-etudj).
  CONFIG_PATH="$TEST_DIR/config.json"
  cat >"$CONFIG_PATH" <<'JSON'
{
  "version": 1,
  "jobs": 2,
  "timeoutSeconds": 300,
  "ignore": [".git/", "fixtures/", "testdata/"],
  "nonUnitLabels": ["integration", "smoke"],
  "languages": [
    {
      "name": "go",
      "markers": ["go.mod"],
      "tools": ["go"],
      "run": {
        "unit": ["go", "test", "./..."],
        "labels": ["go", "test", "-tags", "{labels}", "./..."],
        "all": ["go", "test", "-tags", "{allLabels}", "./..."]
      }
    },
    {
      "name": "bats",
      "markers": ["tests/*.bats"],
      "tools": ["bats", "parallel"],
      "labelPrefix": "type:",
      "run": {
        "unit": ["bats", "--jobs", "{jobs}", "--filter-tags", "{unitExclusion}", "tests/"],
        "labels": ["bats", "--filter-tags", "{label}", "tests/"],
        "all": ["bats", "tests/"]
      }
    },
    {
      "name": "phantom",
      "markers": ["PHANTOM.marker"],
      "tools": ["totally-bogus-tool-xyz"],
      "run": { "unit": ["totally-bogus-tool-xyz"], "labels": ["totally-bogus-tool-xyz"], "all": ["totally-bogus-tool-xyz"] }
    }
  ]
}
JSON
  # $PG_TEST_RUNNER_CONFIG (the documented env-var override), NOT
  # $PG_TEST_RUNNER_DEFAULT_CONFIG: the latter is the BAKED-IN default,
  # which the real assembled/wrapped binary sets unconditionally as a plain
  # (re)assignment inside its own body -- so exporting it from the test
  # process is silently overwritten there and only ever appeared to work
  # via the local wrapper fallback (which has no config injection to
  # collide with). The env var is part of the actual precedence chain and
  # works identically for both SCRIPT shapes.
  export PG_TEST_RUNNER_CONFIG="$CONFIG_PATH"

  GOCACHE="$TEST_DIR/go-cache"
  export GOCACHE
  mkdir -p "$GOCACHE"
}

teardown() {
  cd /
  rm -rf "$TEST_DIR"
  # An `if` (never a bare `[[ ]] && cmd`) so teardown's own exit status is
  # always 0 regardless of which branch runs: bats treats a nonzero
  # teardown return as a TEST FAILURE on its own, independent of the test
  # body -- and TEST_WRAPPER_DIR is UNSET in the nix-check/SCRIPT_UNDER_TEST
  # path (only the local-wrapper fallback sets it), where `[[ -n "" ]]`
  # alone would make `&&`'s short-circuited false the last, nonzero status.
  if [[ -n "${TEST_WRAPPER_DIR:-}" ]]; then
    rm -rf "$TEST_WRAPPER_DIR"
  fi
}

_go_mod() {
  # $1: directory to write a trivial, valid, PASSING go module into (a bare
  # go.mod with no .go files makes `go test ./...` itself exit 1 -- "no
  # packages to test" -- which would be a fixture defect, not a runner one).
  cat >"$1/go.mod" <<'EOF'
module example.com/fixture

go 1.21
EOF
  cat >"$1/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestFixture(t *testing.T) {}
EOF
}

_bats_unit() {
  # $1: directory; writes an unlabeled (= unit, section 1) passing test.
  mkdir -p "$1/tests"
  cat >"$1/tests/unit.bats" <<'EOF'
#!/usr/bin/env bats
@test "unlabeled defaults to unit" { true; }
EOF
}

_bats_tagged() {
  # $1: directory, $2: label. Writes a file-tagged non-unit test.
  #
  # Built with printf, split across pieces, rather than a heredoc spelling
  # the literal marker: bats' OWN static tag-scanner reads THIS SOURCE FILE
  # textually for "# bats file_tags=..." lines when gathering tests, so an
  # unsubstituted "type:$2" placeholder sitting in a heredoc here — destined
  # for the GENERATED fixture file, never executed as a real directive in
  # this file — still trips bats' scanner on THIS file and aborts gathering
  # before any test runs.
  mkdir -p "$1/tests"
  {
    printf '%s\n' '#!/usr/bin/env bats'
    printf '# bats %s=type:%s\n' "file_tags" "$2"
    printf '@test "tagged %s test" { true; }\n' "$2"
  } >"$1/tests/tagged.bats"
}

# --- CLI parsing / mode exclusivity --------------------------------------

@test "--help exits 0 and documents all three modes" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"--files"* ]]
  [[ "$output" == *"--all"* ]]
}

@test "--files and --all are mutually exclusive" {
  run "$SCRIPT" --files --all
  [ "$status" -eq 2 ]
}

@test "--all rejects path arguments" {
  run "$SCRIPT" --all somepath
  [ "$status" -eq 2 ]
}

@test "an unknown option is a usage error" {
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
}

# --- nonexistent / unresolved paths --------------------------------------

@test "a nonexistent path argument exits 2" {
  run "$SCRIPT" "$TEST_DIR/does-not-exist"
  [ "$status" -eq 2 ]
}

@test "a path resolving to no project (upward or downward) exits 12, nothing runs" {
  mkdir -p "$TEST_DIR/repo/empty"
  mkdir "$TEST_DIR/repo/.git"
  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" empty
  [ "$status" -eq 12 ]
}

# --- root-marker repos + dual-marker directories + fixture pruning ------

@test "root-marker + dual-marker + fixture pruning, all via --all" {
  mkdir -p "$TEST_DIR/repo"
  mkdir "$TEST_DIR/repo/.git"
  _go_mod "$TEST_DIR/repo" # the toplevel itself is a legitimate project root

  mkdir -p "$TEST_DIR/repo/dualmark"
  _go_mod "$TEST_DIR/repo/dualmark"
  _bats_unit "$TEST_DIR/repo/dualmark"

  # A fixture tree the default ignore prunes -- even though it carries its
  # own go.mod, it must never surface as a discovered project.
  mkdir -p "$TEST_DIR/repo/lib/tests/fixtures/goversion"
  _go_mod "$TEST_DIR/repo/lib/tests/fixtures/goversion"

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --all
  echo "$output"
  [[ "$output" == *"$TEST_DIR/repo (go)"* || "$output" == *"$TEST_DIR/repo ("*")"* ]]
  [[ "$output" == *"$TEST_DIR/repo/dualmark (go)"* ]]
  [[ "$output" == *"$TEST_DIR/repo/dualmark (bats)"* ]]
  [[ "$output" != *"fixtures"* ]]
}

@test "a query below a self-marked toplevel falls back downward, not vacuously to the root" {
  mkdir -p "$TEST_DIR/repo"
  mkdir "$TEST_DIR/repo/.git"
  _go_mod "$TEST_DIR/repo" # root has its OWN marker

  mkdir -p "$TEST_DIR/repo/packages/x/nested"
  _go_mod "$TEST_DIR/repo/packages/x/nested" # packages/x itself has no marker

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --labels unit packages/x
  echo "$output"
  [[ "$output" == *"$TEST_DIR/repo/packages/x/nested"* ]]
  [[ "$output" != *"$TEST_DIR/repo (go)"* ]]
}

# --- file args vs directory args -----------------------------------------

@test "a FILE argument resolves via its containing directory, same project as the directory itself" {
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _go_mod "$TEST_DIR/repo/proj"

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --labels unit proj/go.mod
  echo "$output"
  [ "$status" -eq 0 ]
  [[ "$output" == *"$TEST_DIR/repo/proj"* ]]
}

# --- --files mode (prek mode) --------------------------------------------

@test "--files: an unowned root-level file contributes nothing, silently, exit 0" {
  mkdir -p "$TEST_DIR/repo"
  mkdir "$TEST_DIR/repo/.git"
  : >"$TEST_DIR/repo/README.md"

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --files README.md
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "--files: a staged file under a pruned subtree contributes nothing" {
  mkdir -p "$TEST_DIR/repo/lib/tests/fixtures/goversion"
  mkdir "$TEST_DIR/repo/.git"
  _go_mod "$TEST_DIR/repo/lib/tests/fixtures/goversion"

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --files lib/tests/fixtures/goversion/go.mod
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "--files: a staged file inside a real project maps to it" {
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _go_mod "$TEST_DIR/repo/proj"

  cd "$TEST_DIR/repo" || return 1
  run "$SCRIPT" --files proj/go.mod
  [[ "$output" == *"$TEST_DIR/repo/proj"* ]]
}

@test "--files outside a repository is a usage error" {
  cd "$TEST_DIR" || return 1
  run "$SCRIPT" --files somefile
  [ "$status" -eq 2 ]
}

# --- label selection: unit default, tagged exclusion, explicit label ----

@test "an unlabeled bats test runs under the default unit tier; a tagged non-unit test does not" {
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _bats_unit "$TEST_DIR/repo/proj"
  _bats_tagged "$TEST_DIR/repo/proj" "integration"

  cd "$TEST_DIR/repo/proj" || return 1
  run "$SCRIPT" --labels unit .
  echo "$output"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS]"* ]]
}

@test "an explicit non-unit label selects only that tagged test" {
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _bats_unit "$TEST_DIR/repo/proj"
  _bats_tagged "$TEST_DIR/repo/proj" "integration"

  cd "$TEST_DIR/repo/proj" || return 1
  run "$SCRIPT" --labels integration .
  echo "$output"
  [ "$status" -eq 0 ]
}

@test "--labels all is unfiltered" {
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _bats_unit "$TEST_DIR/repo/proj"
  _bats_tagged "$TEST_DIR/repo/proj" "integration"

  cd "$TEST_DIR/repo/proj" || return 1
  run "$SCRIPT" --labels all .
  [ "$status" -eq 0 ]
}

# --- tool-missing (exit 11) and its precedence under exit 10 ------------

@test "a required tool missing from PATH is a distinct, named failure (exit 11)" {
  # "phantom" declares a tool ("totally-bogus-tool-xyz") that can never exist
  # on PATH regardless of environment, so this needs no PATH manipulation.
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  : >"$TEST_DIR/repo/proj/PHANTOM.marker"

  cd "$TEST_DIR/repo/proj" || return 1
  run "$SCRIPT" --labels unit .
  [ "$status" -eq 11 ]
  [[ "$output" == *"tool missing"* ]]
  [[ "$output" == *"totally-bogus-tool-xyz"* ]]
}

# --- config resolution precedence ----------------------------------------

@test "--config flag overrides \$PG_TEST_RUNNER_CONFIG" {
  echo '{"version":2}' >"$TEST_DIR/bad-env-config.json"
  mkdir -p "$TEST_DIR/repo/proj"
  mkdir "$TEST_DIR/repo/.git"
  _bats_unit "$TEST_DIR/repo/proj"

  cd "$TEST_DIR/repo/proj" || return 1
  PG_TEST_RUNNER_CONFIG="$TEST_DIR/bad-env-config.json" run "$SCRIPT" --config "$CONFIG_PATH" --labels unit .
  [ "$status" -eq 0 ]
}

@test "an invalid config is exit 13, not silently ignored" {
  echo 'not json at all' >"$TEST_DIR/broken.json"
  run "$SCRIPT" --config "$TEST_DIR/broken.json" .
  [ "$status" -eq 13 ]
}

# --- projectTimeouts: per-project timeoutSeconds override (bead pg2-x86sp) ---
# A one-language config whose "suite" is `sleep 3` under a 1s global cap, so a
# project is killed unless an override names it. Proves the override raises
# ONLY the named project's cap and leaves every other project on the global one.

_timeout_config() {
  # $1: the projectTimeouts JSON value ("" = key absent)
  local extra=""
  [[ -n $1 ]] && extra="\"projectTimeouts\": $1,"
  cat >"$TEST_DIR/timeout-config.json" <<JSON
{
  "version": 1,
  "jobs": 1,
  "timeoutSeconds": 1,
  $extra
  "ignore": [".git/"],
  "nonUnitLabels": [],
  "languages": [
    {
      "name": "sleeper",
      "markers": ["SLEEP.marker"],
      "tools": ["bash"],
      "run": { "unit": ["bash", "-c", "sleep 3"], "labels": ["bash", "-c", "sleep 3"], "all": ["bash", "-c", "sleep 3"] }
    }
  ]
}
JSON
}

_timeout_repo() {
  mkdir -p "$TEST_DIR/trepo/.git" "$TEST_DIR/trepo/packages/slow" "$TEST_DIR/trepo/packages/other"
  : >"$TEST_DIR/trepo/packages/slow/SLEEP.marker"
  : >"$TEST_DIR/trepo/packages/other/SLEEP.marker"
}

@test "without projectTimeouts the global timeoutSeconds cap kills a slow project (exit 10)" {
  _timeout_config ""
  _timeout_repo
  cd "$TEST_DIR/trepo" || return 1
  run "$SCRIPT" --config "$TEST_DIR/timeout-config.json" --labels unit packages/slow
  [ "$status" -eq 10 ]
  [[ "$output" == *"timed out after 1s cap"* ]]
}

@test "a projectTimeouts entry raises only the named project's cap" {
  _timeout_config '{"packages/slow": 20}'
  _timeout_repo
  cd "$TEST_DIR/trepo" || return 1
  run "$SCRIPT" --config "$TEST_DIR/timeout-config.json" --labels unit packages/slow
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS]"* ]]
  # a sibling project the entry does not name still hits the global 1s cap
  run "$SCRIPT" --config "$TEST_DIR/timeout-config.json" --labels unit packages/other
  [ "$status" -eq 10 ]
  [[ "$output" == *"timed out after 1s cap"* ]]
}

@test "an overridden project that exceeds its own larger cap still fails, naming that cap" {
  _timeout_config '{"packages/slow": 2}'
  _timeout_repo
  cd "$TEST_DIR/trepo" || return 1
  run "$SCRIPT" --config "$TEST_DIR/timeout-config.json" --labels unit packages/slow
  [ "$status" -eq 10 ]
  [[ "$output" == *"timed out after 2s cap"* ]]
}

@test "a malformed projectTimeouts is exit 13, not a silent fallback" {
  _timeout_repo
  cd "$TEST_DIR/trepo" || return 1
  local bad
  for bad in '{"packages/slow": 0}' '{"packages/slow": "20"}' '{"packages/slow": 1.5}' '{"/packages/slow": 20}' '{"packages/slow/": 20}' '[20]'; do
    _timeout_config "$bad"
    run "$SCRIPT" --config "$TEST_DIR/timeout-config.json" --labels unit packages/slow
    [ "$status" -eq 13 ] || { echo "expected 13 for $bad, got $status: $output" >&2; return 1; }
    [[ "$output" == *"projectTimeouts"* ]]
  done
}

# --- host-wide run slots + nice (bead pg2-r9ly8) -------------------------
# Two separate pg-test-runner PROCESSES sharing one lock root must not run
# their projects at the same time when maxConcurrentRuns is 1; the cap is
# per host, not per process.

_slots_config() {
  # $1: extra top-level JSON members (with trailing comma), e.g. '"maxConcurrentRuns": 1,'
  cat >"$TEST_DIR/slots-config.json" <<JSON
{
  "version": 1,
  "jobs": 1,
  "timeoutSeconds": 30,
  $1
  "ignore": [".git/"],
  "nonUnitLabels": [],
  "languages": [
    {
      "name": "logger",
      "markers": ["LOG.marker"],
      "tools": ["bash"],
      "run": {
        "unit": ["bash", "-c", "echo start >>\"\$LOG\"; sleep 1; echo end >>\"\$LOG\""],
        "labels": ["true"],
        "all": ["true"]
      }
    }
  ]
}
JSON
}

_slots_repo() {
  mkdir -p "$TEST_DIR/srepo/.git" "$TEST_DIR/srepo/packages/a" "$TEST_DIR/srepo/packages/b"
  : >"$TEST_DIR/srepo/packages/a/LOG.marker"
  : >"$TEST_DIR/srepo/packages/b/LOG.marker"
}

@test "maxConcurrentRuns=1 serialises two separate pg-test-runner processes" {
  _slots_config '"maxConcurrentRuns": 1, "niceLevel": 1, "slotWaitSeconds": 60,'
  _slots_repo
  export LOG="$TEST_DIR/slots.log" PG_TEST_RUNNER_LOCK_ROOT="$TEST_DIR/slot-root"
  cd "$TEST_DIR/srepo" || return 1
  "$SCRIPT" --config "$TEST_DIR/slots-config.json" --labels unit packages/a >/dev/null &
  p1=$!
  "$SCRIPT" --config "$TEST_DIR/slots-config.json" --labels unit packages/b >/dev/null &
  p2=$!
  wait "$p1"
  wait "$p2"
  [ "$(tr '\n' ' ' <"$LOG")" = "start end start end " ]
}

@test "the environment can lift the cap for one run (PG_TEST_RUNNER_MAX_CONCURRENT_RUNS=0)" {
  _slots_config '"maxConcurrentRuns": 1, "slotWaitSeconds": 60,'
  _slots_repo
  export LOG="$TEST_DIR/slots.log" PG_TEST_RUNNER_LOCK_ROOT="$TEST_DIR/slot-root" PG_TEST_RUNNER_MAX_CONCURRENT_RUNS=0
  cd "$TEST_DIR/srepo" || return 1
  "$SCRIPT" --config "$TEST_DIR/slots-config.json" --labels unit packages/a >/dev/null &
  p1=$!
  "$SCRIPT" --config "$TEST_DIR/slots-config.json" --labels unit packages/b >/dev/null &
  p2=$!
  wait "$p1"
  wait "$p2"
  [ "$(tr '\n' ' ' <"$LOG")" = "start start end end " ]
  [ ! -e "$TEST_DIR/slot-root" ]
}

@test "a malformed run limit is exit 13, not a silent fallback" {
  _slots_repo
  cd "$TEST_DIR/srepo" || return 1
  local bad
  for bad in '"maxConcurrentRuns": -1,' '"maxConcurrentRuns": "2",' '"niceLevel": 20,' '"slotWaitSeconds": 0.5,'; do
    _slots_config "$bad"
    run "$SCRIPT" --config "$TEST_DIR/slots-config.json" --labels unit packages/a
    [ "$status" -eq 13 ] || { echo "expected 13 for $bad, got $status: $output" >&2; return 1; }
    [[ "$output" == *"maxConcurrentRuns"* ]]
  done
}

# --- project concurrency (bead pg2-nnf7z) --------------------------------
# One run may start several projects at the same time (projectConcurrency);
# output is replayed in discovery order, the exit status keeps its precedence,
# and the host-wide slot cap still bounds the real parallelism.

_conc_config() {
  # $1: extra top-level JSON members (with trailing comma)
  cat >"$TEST_DIR/conc-config.json" <<JSON
{
  "version": 1,
  "jobs": 1,
  "timeoutSeconds": 30,
  "maxConcurrentRuns": 0,
  $1
  "ignore": [".git/"],
  "nonUnitLabels": [],
  "languages": [
    {
      "name": "logger",
      "markers": ["LOG.marker"],
      "tools": ["bash"],
      "run": {
        "unit": ["bash", "-c", "n=\$(basename \"\$PWD\"); echo start-\$n >>\"\$LOG\"; sleep 1; echo end-\$n >>\"\$LOG\"; echo output-of-\$n; [ ! -e FAIL ]"],
        "labels": ["true"],
        "all": ["true"]
      }
    }
  ]
}
JSON
}

# two projects (a, b) by default; a third (c) that fails when asked
_conc_repo() {
  local p
  for p in a b c; do
    mkdir -p "$TEST_DIR/crepo/packages/$p"
    : >"$TEST_DIR/crepo/packages/$p/LOG.marker"
  done
  mkdir -p "$TEST_DIR/crepo/.git"
  export LOG="$TEST_DIR/conc.log" PG_TEST_RUNNER_LOCK_ROOT="$TEST_DIR/conc-slot-root"
  cd "$TEST_DIR/crepo" || return 1
}

@test "projectConcurrency defaults to one project at a time" {
  _conc_config ''
  _conc_repo
  run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a packages/b
  [ "$status" -eq 0 ]
  [ "$(tr '\n' ' ' <"$LOG")" = "start-a end-a start-b end-b " ]
}

# Regression guard (bead pg2-27c6c): the suite must not depend on the caller's
# PG_TEST_RUNNER_* environment. Re-run the two tests that assume the knobs are
# unset in a nested bats with the knobs exported in the OUTER environment; the
# anchored --filter keeps this guard test out of the nested run (no recursion).
@test "the suite is hermetic against exported PG_TEST_RUNNER_* knobs" {
  command -v bats >/dev/null || skip "bats not on PATH"
  # a real directory: a nonexistent TMP_ROOT would make the runner silently fall
  # back to serial projects and mask a leaked PROJECT_CONCURRENCY
  mkdir -p "$TEST_DIR/leaked-tmp-root"
  PG_TEST_RUNNER_PROJECT_CONCURRENCY=2 \
    PG_TEST_RUNNER_MAX_CONCURRENT_RUNS=0 \
    PG_TEST_RUNNER_NICE_LEVEL=7 \
    PG_TEST_RUNNER_SLOT_WAIT_SECONDS=1 \
    PG_TEST_RUNNER_LOCK_ROOT="$TEST_DIR/leaked-lock-root" \
    PG_TEST_RUNNER_TMP_ROOT="$TEST_DIR/leaked-tmp-root" \
    run bats --filter '^(projectConcurrency defaults to one project at a time|maxConcurrentRuns=1 serialises two separate pg-test-runner processes)$' "$BATS_TEST_FILENAME"
  [ "$status" -eq 0 ] || { echo "$output" >&2; return 1; }
  [[ "$output" == *"projectConcurrency defaults to one project at a time"* ]]
  [[ "$output" == *"maxConcurrentRuns=1 serialises two separate"* ]]
}

@test "projectConcurrency=2 overlaps two projects and replays output in discovery order" {
  _conc_config '"projectConcurrency": 2,'
  _conc_repo
  run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a packages/b
  [ "$status" -eq 0 ]
  # both started before either ended
  [ "$(sed -n '1,2p' "$LOG" | sed 's/-.*//' | tr '\n' ' ')" = "start start " ]
  [ "$(sed -n '3,4p' "$LOG" | sed 's/-.*//' | tr '\n' ' ')" = "end end " ]
  # the replayed output is one project after the other, in the order given
  [ "$(grep -c . <<<"$output")" -eq 4 ]
  [ "$(sed -n 1p <<<"$output")" = "output-of-a" ]
  [[ "$(sed -n 2p <<<"$output")" == "[PASS] "*"packages/a (logger)"* ]]
  [ "$(sed -n 3p <<<"$output")" = "output-of-b" ]
  [[ "$(sed -n 4p <<<"$output")" == "[PASS] "*"packages/b (logger)"* ]]
}

@test "projectConcurrency=2: a failing project is exit 10 and the others still run to completion" {
  _conc_config '"projectConcurrency": 2,'
  _conc_repo
  : >"$TEST_DIR/crepo/packages/a/FAIL"
  run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a packages/b packages/c
  [ "$status" -eq 10 ]
  [[ "$output" == *"[FAIL] "*"packages/a (logger)"* ]]
  [[ "$output" == *"[PASS] "*"packages/b (logger)"* ]]
  [[ "$output" == *"[PASS] "*"packages/c (logger)"* ]]
  [ "$(grep -c '^end-' "$LOG")" -eq 3 ]
}

@test "maxConcurrentRuns=1 still serialises the projects of one run when projectConcurrency=2" {
  _conc_config '"projectConcurrency": 2, "maxConcurrentRuns": 1, "slotWaitSeconds": 60,'
  _conc_repo
  run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a packages/b
  [ "$status" -eq 0 ]
  [ "$(sed 's/-.*//' "$LOG" | tr '\n' ' ')" = "start end start end " ]
}

@test "PG_TEST_RUNNER_PROJECT_CONCURRENCY overrides the config; 0 is a usage error" {
  _conc_config '"projectConcurrency": 2,'
  _conc_repo
  PG_TEST_RUNNER_PROJECT_CONCURRENCY=1 run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a packages/b
  [ "$status" -eq 0 ]
  [ "$(sed 's/-.*//' "$LOG" | tr '\n' ' ')" = "start end start end " ]
  PG_TEST_RUNNER_PROJECT_CONCURRENCY=0 run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a
  [ "$status" -eq 2 ]
  [[ "$output" == *"PG_TEST_RUNNER_PROJECT_CONCURRENCY"* ]]
}

@test "a malformed projectConcurrency is exit 13, not a silent fallback" {
  _conc_repo
  local bad
  for bad in '"projectConcurrency": 0,' '"projectConcurrency": -1,' '"projectConcurrency": "2",' '"projectConcurrency": 1.5,'; do
    _conc_config "$bad"
    run "$SCRIPT" --config "$TEST_DIR/conc-config.json" --labels unit packages/a
    [ "$status" -eq 13 ] || { echo "expected 13 for $bad, got $status: $output" >&2; return 1; }
    [[ "$output" == *"projectConcurrency"* ]]
  done
}

# NOTE on the PG_TEST_RUNNER_JQ_BIN/PG_TEST_RUNNER_TIMEOUT_BIN ordering bug
# (bead pg2-jcqar): mkBashScript's config injection is an UNCONDITIONAL
# (re)assignment inside the assembled script, so it is not actually
# environment-overridable in the real wrapped binary -- a bats test trying
# to override it from the calling environment tests something that was
# never a supported seam and is either vacuous (falls back to a working
# bare "jq" either way, since jq is on PATH in this suite's own sandbox) or
# actively wrong (the override is silently discarded by design). The real
# regression guard for "the read happens before the config line runs" is
# mkBashScript's OWN shellcheck pass on the assembled artifact -- SC2034
# fires on exactly this shape, and did during development -- so no
# additional bats test is needed here.

# --- --files: skip a project whose changed files are no test input (pg2-41fzsf) --
# The `gosuite` language below mirrors the shipped go language's `inputs` rules
# (modules/pg-test-runner/config.nix) but its "suite" only drops a marker file,
# so a test can tell "ran" from "skipped" without compiling Go. Everything else
# is the REAL runner, config validation and --files resolution included.

_skip_config() {
  # $1: projectExtraPaths JSON value ("" = key absent)
  local extra=""
  [[ -n ${1:-} ]] && extra="\"projectExtraPaths\": $1,"
  cat >"$TEST_DIR/skip-config.json" <<JSON
{
  "version": 1,
  "jobs": 1,
  "timeoutSeconds": 60,
  $extra
  "ignore": [".git/", "fixtures/", "testdata/"],
  "nonUnitLabels": [],
  "languages": [
    {
      "name": "gosuite",
      "markers": ["go.mod"],
      "tools": ["bash"],
      "inputs": {
        "names": ["*.go", "go.mod", "go.sum", "go.work", "go.work.sum"],
        "dirs": ["testdata", "vendor"],
        "goEmbed": true
      },
      "run": {
        "unit": ["bash", "-c", "touch ran.marker"],
        "labels": ["bash", "-c", "touch ran.marker"],
        "all": ["bash", "-c", "touch ran.marker"]
      }
    },
    {
      "name": "plainsuite",
      "markers": ["PLAIN.marker"],
      "tools": ["bash"],
      "run": {
        "unit": ["bash", "-c", "touch ran.marker"],
        "labels": ["bash", "-c", "touch ran.marker"],
        "all": ["bash", "-c", "touch ran.marker"]
      }
    }
  ]
}
JSON
}

_skip_repo() {
  # A repo with one Go project at proj/ holding source, docs and assets.
  mkdir -p "$TEST_DIR/srepo/.git" "$TEST_DIR/srepo/proj/internal/x/testdata" \
    "$TEST_DIR/srepo/proj/docs" "$TEST_DIR/srepo/proj/assets"
  _go_mod "$TEST_DIR/srepo/proj"
  : >"$TEST_DIR/srepo/proj/README.md"
  : >"$TEST_DIR/srepo/proj/docs/rules.yaml"
  : >"$TEST_DIR/srepo/proj/assets/tpl.txt"
  : >"$TEST_DIR/srepo/proj/internal/x/testdata/case.json"
}

# $@: changed files (repo-relative). Runs the real runner in --files mode.
_skip_run() {
  cd "$TEST_DIR/srepo" || return 1
  rm -f proj/ran.marker
  run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit --files "$@"
  echo "$output"
}

_assert_skipped() {
  [ "$status" -eq 0 ]
  [[ "$output" == *"[SKIP] $TEST_DIR/srepo/proj (gosuite)"* ]]
  [[ "$output" == *"suite not run"* ]]
  [[ "$output" != *"[PASS]"* ]]
  [ ! -e "$TEST_DIR/srepo/proj/ran.marker" ]
}

_assert_ran() {
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS] $TEST_DIR/srepo/proj (gosuite)"* ]]
  [[ "$output" != *"suite not run"* ]]
  [ -e "$TEST_DIR/srepo/proj/ran.marker" ]
}

@test "--files: only non-Go files under a Go project skip its suite and say so" {
  _skip_config ""
  _skip_repo
  _skip_run proj/README.md proj/docs/rules.yaml
  _assert_skipped
  [[ "$output" == *"none of the 2 changed file(s)"* ]]
}

@test "--files: the skip is one line per skipped project" {
  _skip_config ""
  _skip_repo
  _skip_run proj/README.md proj/docs/rules.yaml proj/assets/tpl.txt
  _assert_skipped
  [ "$(grep -c '\[SKIP\]' <<<"$output")" -eq 1 ]
}

@test "--files: a .go source file still runs the suite" {
  _skip_config ""
  _skip_repo
  _skip_run proj/fixture_test.go
  _assert_ran
}

@test "--files: a non-test .go file still runs the suite" {
  _skip_config ""
  _skip_repo
  _skip_run proj/internal/x/impl.go
  _assert_ran
}

@test "--files: go.mod still runs the suite" {
  _skip_config ""
  _skip_repo
  _skip_run proj/go.mod
  _assert_ran
}

@test "--files: go.sum still runs the suite" {
  _skip_config ""
  _skip_repo
  _skip_run proj/go.sum
  _assert_ran
}

@test "--files: one Go file among non-Go files runs the suite and prints no skip" {
  _skip_config ""
  _skip_repo
  _skip_run proj/README.md proj/docs/rules.yaml proj/fixture_test.go
  _assert_ran
}

@test "--files: a deleted Go file (path only, not on disk) still runs the suite" {
  _skip_config ""
  _skip_repo
  _skip_run proj/internal/x/gone.go
  _assert_ran
}

@test "--files: a file under testdata/ counts although testdata/ is an ignored directory" {
  _skip_config ""
  _skip_repo
  _skip_run proj/internal/x/testdata/case.json
  _assert_ran
}

@test "--files: a declared projectExtraPaths entry counts, an undeclared sibling does not" {
  _skip_config '{"proj": ["assets"]}'
  _skip_repo
  _skip_run proj/assets/tpl.txt
  _assert_ran
  _skip_run proj/docs/rules.yaml
  _assert_skipped
}

@test "--files: projectExtraPaths matches a trailing run of the project path and supports globs" {
  _skip_config '{"srepo/proj": ["docs/*.yaml"]}'
  _skip_repo
  _skip_run proj/docs/rules.yaml
  _assert_ran
  _skip_run proj/README.md
  _assert_skipped
}

@test "--files: without projectExtraPaths the same asset skips" {
  _skip_config ""
  _skip_repo
  _skip_run proj/assets/tpl.txt
  _assert_skipped
}

_embed_source() {
  # $1: the //go:embed directive line(s) to write into proj/embed.go
  {
    printf 'package fixture\n\nimport "embed"\n\n'
    printf '%s\n' "$1"
    printf 'var assets embed.FS\n'
  } >"$TEST_DIR/srepo/proj/embed.go"
}

@test "--files: a file named by a //go:embed glob counts; one it does not name does not" {
  _skip_config ""
  _skip_repo
  _embed_source '//go:embed assets/*.txt'
  _skip_run proj/assets/tpl.txt
  _assert_ran
  _skip_run proj/docs/rules.yaml
  _assert_skipped
}

@test "--files: a //go:embed directory covers every file below it" {
  _skip_config ""
  _skip_repo
  mkdir -p "$TEST_DIR/srepo/proj/assets/deep"
  : >"$TEST_DIR/srepo/proj/assets/deep/more.bin"
  _embed_source '//go:embed all:assets'
  _skip_run proj/assets/deep/more.bin
  _assert_ran
}

@test "--files: quoted //go:embed patterns (with a space) and several patterns per line parse" {
  _skip_config ""
  _skip_repo
  _embed_source '//go:embed "docs/my file.txt" `assets/raw.txt` README.md'
  _skip_run "proj/docs/my file.txt"
  _assert_ran
  _skip_run proj/assets/raw.txt
  _assert_ran
  _skip_run proj/README.md
  _assert_ran
  _skip_run proj/docs/rules.yaml
  _assert_skipped
}

@test "--files: a //go:embed in a subpackage is relative to that package's directory" {
  _skip_config ""
  _skip_repo
  mkdir -p "$TEST_DIR/srepo/proj/internal/x/tpl"
  : >"$TEST_DIR/srepo/proj/internal/x/tpl/a.tmpl"
  {
    printf 'package x\n\nimport "embed"\n\n//go:embed tpl\nvar tpl embed.FS\n'
  } >"$TEST_DIR/srepo/proj/internal/x/embed.go"
  _skip_run proj/internal/x/tpl/a.tmpl
  _assert_ran
  # the same relative name at the project root is NOT what the directive names
  mkdir -p "$TEST_DIR/srepo/proj/tpl"
  : >"$TEST_DIR/srepo/proj/tpl/a.tmpl"
  _skip_run proj/tpl/a.tmpl
  _assert_skipped
}

@test "--files: an //go:embed directive it cannot parse makes every file count (run when in doubt)" {
  _skip_config ""
  _skip_repo
  _embed_source '//go:embed "unterminated'
  _skip_run proj/README.md
  _assert_ran
}

@test "--files: a bare //go:embed with no pattern is unparseable, so the suite runs" {
  _skip_config ""
  _skip_repo
  _embed_source '//go:embed'
  _skip_run proj/README.md
  _assert_ran
}

@test "--files: one project skipped and another run in the same invocation" {
  _skip_config ""
  _skip_repo
  mkdir -p "$TEST_DIR/srepo/other"
  _go_mod "$TEST_DIR/srepo/other"
  cd "$TEST_DIR/srepo" || return 1
  rm -f proj/ran.marker other/ran.marker
  run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit --files proj/README.md other/fixture_test.go
  echo "$output"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[SKIP] $TEST_DIR/srepo/proj (gosuite)"* ]]
  [[ "$output" == *"[PASS] $TEST_DIR/srepo/other (gosuite)"* ]]
  [ ! -e proj/ran.marker ]
  [ -e other/ran.marker ]
}

@test "--files: a language without inputs rules runs on any changed file" {
  _skip_config ""
  mkdir -p "$TEST_DIR/srepo/.git" "$TEST_DIR/srepo/plain"
  : >"$TEST_DIR/srepo/plain/PLAIN.marker"
  : >"$TEST_DIR/srepo/plain/README.md"
  cd "$TEST_DIR/srepo" || return 1
  run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit --files plain/README.md
  echo "$output"
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS] $TEST_DIR/srepo/plain (plainsuite)"* ]]
  [ -e plain/ran.marker ]
}

@test "path and --all modes never skip, whatever the changed files are" {
  _skip_config ""
  _skip_repo
  cd "$TEST_DIR/srepo" || return 1
  run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit proj
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS] $TEST_DIR/srepo/proj (gosuite)"* ]]
  rm -f proj/ran.marker
  run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit --all
  [ "$status" -eq 0 ]
  [[ "$output" == *"[PASS] $TEST_DIR/srepo/proj (gosuite)"* ]]
  [[ "$output" != *"suite not run"* ]]
}

@test "malformed projectExtraPaths or inputs is exit 13, not a silent fallback" {
  _skip_repo
  cd "$TEST_DIR/srepo" || return 1
  local bad
  for bad in '{"proj": "assets"}' '{"proj": [""]}' '{"proj": ["/abs"]}' '{"proj": [1]}' '{"/proj": ["a"]}' '["a"]'; do
    _skip_config "$bad"
    run "$SCRIPT" --config "$TEST_DIR/skip-config.json" --labels unit --files proj/README.md
    [ "$status" -eq 13 ] || { echo "expected 13 for $bad, got $status: $output" >&2; return 1; }
    [[ "$output" == *"projectExtraPaths"* ]]
  done
  _skip_config ""
  for bad in '"names": "*.go"' '"dirs": ["a/b"]' '"goEmbed": "yes"' '"names": [""]'; do
    jq --argjson i "{$bad}" '.languages[0].inputs = $i' "$TEST_DIR/skip-config.json" >"$TEST_DIR/bad.json"
    run "$SCRIPT" --config "$TEST_DIR/bad.json" --labels unit --files proj/README.md
    [ "$status" -eq 13 ] || { echo "expected 13 for inputs $bad, got $status: $output" >&2; return 1; }
    [[ "$output" == *"inputs"* ]]
  done
}

@test "the shipped go language config carries the inputs rules and an empty projectExtraPaths" {
  local cfg="$BATS_TEST_DIRNAME/../../config.nix" json
  [ -f "$cfg" ] || skip "config.nix not beside the sources (nix sandbox layout)"
  command -v nix-instantiate >/dev/null 2>&1 || skip "nix-instantiate unavailable"
  json="$(nix-instantiate --eval --strict --json "$cfg")"
  jq -e '.languages[] | select(.name == "go") | .inputs | (.names | index("*.go")) != null and (.names | index("go.mod")) != null and (.names | index("go.sum")) != null and (.dirs | index("testdata")) != null and .goEmbed == true' <<<"$json"
  jq -e '.projectExtraPaths == {}' <<<"$json"
}
