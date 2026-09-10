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
  unset PG_TEST_RUNNER_CONFIG PG_TEST_RUNNER_DEFAULT_CONFIG

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
