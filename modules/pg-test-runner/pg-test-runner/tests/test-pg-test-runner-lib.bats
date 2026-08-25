#!/usr/bin/env bats
# Unit suite for pg-test-runner.bash: path/ignore utilities, discovery
# primitives, and placeholder substitution, exercised WITHOUT going through
# argument parsing. These are pure(ish) functions over the filesystem and a
# config JSON fixture, so this suite runs fast and needs no bats/go/uv/npm
# under test.

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
}

setup() {
  # shellcheck disable=SC1091  # runtime-computed path (SCRIPTS_DIR)
  source "$SCRIPTS_DIR/pg-test-runner.bash"

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  cd "$TEST_DIR" || return 1

  CONFIG_PATH="$TEST_DIR/config.json"
  cat >"$CONFIG_PATH" <<'JSON'
{
  "version": 1,
  "jobs": 0,
  "timeoutSeconds": 5,
  "ignore": [".git/", "node_modules/", "fixtures/", "lib/bash-builders-tests/"],
  "nonUnitLabels": ["integration", "smoke", "contract", "hostile"],
  "languages": [
    {
      "name": "go",
      "markers": ["go.mod"],
      "tools": ["go"],
      "run": { "unit": ["go", "test"], "labels": ["go", "test", "-tags", "{labels}"], "all": ["go", "test", "-tags", "{allLabels}"] }
    },
    {
      "name": "js",
      "markers": ["package.json"],
      "tools": ["npm"],
      "run": {
        "probe": { "unit": ["jq", "-e", ".scripts[\"test:unit\"]", "package.json"] },
        "unit": ["npm", "run", "test:unit"],
        "labels": ["npm", "run", "test:{label}"],
        "all": ["npm", "test"]
      }
    },
    {
      "name": "bats",
      "markers": ["tests/*.bats"],
      "tools": ["bats"],
      "labelPrefix": "type:",
      "run": {
        "unit": ["bats", "--jobs", "{jobs}", "--filter-tags", "{unitExclusion}", "tests/"],
        "labels": ["bats", "--filter-tags", "{label}", "tests/"],
        "all": ["bats", "tests/"]
      }
    }
  ]
}
JSON

  PTR_CONFIG="$CONFIG_PATH"
  ptr_load_languages
  # shellcheck disable=SC2034  # read by ptr_is_ignored/ptr_unit_exclusion in the sourced library, not this file
  mapfile -t IGNORE_PATTERNS < <("$PTR_JQ" -r '.ignore[]?' "$PTR_CONFIG")
  # shellcheck disable=SC2034  # read by ptr_unit_exclusion/ptr_all_labels in the sourced library, not this file
  NON_UNIT_LABELS_JSON="$("$PTR_JQ" -c '.nonUnitLabels // []' "$PTR_CONFIG")"
}

teardown() {
  cd /
  rm -rf "$TEST_DIR"
}

# --- ptr_abspath / ptr_relpath -----------------------------------------

@test "ptr_abspath collapses . and .. lexically without requiring existence" {
  result="$(ptr_abspath "/a/b/../c/./d")"
  [ "$result" = "/a/c/d" ]
}

@test "ptr_abspath resolves a relative path against PWD" {
  cd "$TEST_DIR"
  result="$(ptr_abspath "sub/dir")"
  [ "$result" = "$TEST_DIR/sub/dir" ]
}

@test "ptr_relpath strips the root prefix" {
  result="$(ptr_relpath "/repo" "/repo/lib/thing")"
  [ "$result" = "lib/thing" ]
}

@test "ptr_relpath of the root itself is empty" {
  result="$(ptr_relpath "/repo" "/repo")"
  [ "$result" = "" ]
}

# --- ptr_find_toplevel ---------------------------------------------------

@test "ptr_find_toplevel finds a nearest ancestor .git DIRECTORY" {
  mkdir -p "$TEST_DIR/repo/a/b"
  mkdir "$TEST_DIR/repo/.git"
  result="$(ptr_find_toplevel "$TEST_DIR/repo/a/b")"
  [ "$result" = "$TEST_DIR/repo" ]
}

@test "ptr_find_toplevel finds a .git FILE (linked worktree)" {
  mkdir -p "$TEST_DIR/repo/a"
  echo "gitdir: /elsewhere" >"$TEST_DIR/repo/.git"
  result="$(ptr_find_toplevel "$TEST_DIR/repo/a")"
  [ "$result" = "$TEST_DIR/repo" ]
}

@test "ptr_find_toplevel returns failure with no .git ancestor" {
  mkdir -p "$TEST_DIR/norepo/a"
  run ptr_find_toplevel "$TEST_DIR/norepo/a"
  [ "$status" -eq 1 ]
}

@test "ptr_find_toplevel works from a nonexistent starting path (deleted file case)" {
  mkdir -p "$TEST_DIR/repo"
  mkdir "$TEST_DIR/repo/.git"
  result="$(ptr_find_toplevel "$TEST_DIR/repo/deleted/dir/that/never/existed")"
  [ "$result" = "$TEST_DIR/repo" ]
}

# --- ptr_is_ignored (gitignore-style, subtree pruning both directions) --

@test "an unanchored pattern matches the basename at any depth" {
  run ptr_is_ignored "deeply/nested/node_modules"
  [ "$status" -eq 0 ]
}

@test "an anchored pattern (contains a slash) matches only that exact relative path" {
  run ptr_is_ignored "lib/bash-builders-tests"
  [ "$status" -eq 0 ]
  run ptr_is_ignored "other/lib/bash-builders-tests"
  [ "$status" -eq 1 ]
}

@test "a path under an ignored ancestor is ignored too (subtree pruning)" {
  # fixtures/ is unanchored; goversion sits two levels below it, but the whole
  # subtree stays pruned once fixtures/ itself matches.
  run ptr_is_ignored "lib/tests/fixtures/goversion"
  [ "$status" -eq 0 ]
}

@test "the scan root itself (empty relpath) is never ignored" {
  run ptr_is_ignored ""
  [ "$status" -eq 1 ]
}

@test "an unrelated path is not ignored" {
  run ptr_is_ignored "modules/pn"
  [ "$status" -eq 1 ]
}

# --- ptr_languages_matching_dir (dual-marker directories) ----------------

@test "a directory matching several languages' markers yields all of them" {
  mkdir -p "$TEST_DIR/dualmark"
  : >"$TEST_DIR/dualmark/go.mod"
  echo '{}' >"$TEST_DIR/dualmark/package.json"
  mapfile -t got < <(ptr_languages_matching_dir "$TEST_DIR/dualmark")
  [ "${#got[@]}" -eq 2 ]
  [[ " ${got[*]} " == *" go "* ]]
  [[ " ${got[*]} " == *" js "* ]]
}

@test "a directory with no marker matches no language" {
  mkdir -p "$TEST_DIR/empty"
  mapfile -t got < <(ptr_languages_matching_dir "$TEST_DIR/empty")
  [ "${#got[@]}" -eq 0 ]
}

# --- ptr_downward_scan: never follows symlinks, prunes both directions --

@test "ptr_downward_scan does not follow a symlinked directory" {
  mkdir -p "$TEST_DIR/real-target"
  : >"$TEST_DIR/real-target/go.mod"
  mkdir "$TEST_DIR/root"
  ln -s "$TEST_DIR/real-target" "$TEST_DIR/root/linked"
  result="$(ptr_downward_scan "$TEST_DIR/root" "$TEST_DIR/root")"
  [ -z "$result" ]
}

@test "ptr_downward_scan does not stop at a discovered project root -- nesting still descends" {
  mkdir -p "$TEST_DIR/outer/inner"
  : >"$TEST_DIR/outer/go.mod"
  : >"$TEST_DIR/outer/inner/go.mod"
  result="$(ptr_downward_scan "$TEST_DIR/outer" "$TEST_DIR/outer")"
  [[ "$result" == *"$TEST_DIR/outer"$'\t'"go"* ]]
  [[ "$result" == *"$TEST_DIR/outer/inner"$'\t'"go"* ]]
}

# --- PTR_JQ / PTR_TIMEOUT default (direct-library context) --------------
#
# The REAL resolution of PG_TEST_RUNNER_JQ_BIN/PG_TEST_RUNNER_TIMEOUT_BIN
# deliberately lives in pg-test-runner.sh, not this file -- see that file's
# comment for why (a followed `source` of this library is the wrong place
# for shellcheck to see the read as using the config lines' assignment).
# This library only needs a sane default for a direct-library unit test like
# this one, which sources pg-test-runner.bash without ever loading
# pg-test-runner.sh.

@test "PTR_JQ/PTR_TIMEOUT default to the bare command names" {
  [ "$PTR_JQ" = "jq" ]
  [ "$PTR_TIMEOUT" = "timeout" ]
}

# --- ptr_render_token / ptr_unit_exclusion / ptr_all_labels --------------

@test "ptr_render_token substitutes every placeholder, substring-level" {
  result="$(ptr_render_token "test:{label}-{jobs}" "4" "" "unit" "" "")"
  [ "$result" = "test:unit-4" ]
}

@test "ptr_unit_exclusion derives the negation list from nonUnitLabels with the language prefix" {
  result="$(ptr_unit_exclusion "type:")"
  [ "$result" = "!type:integration,!type:smoke,!type:contract,!type:hostile" ]
}

@test "ptr_all_labels joins nonUnitLabels without a prefix" {
  result="$(ptr_all_labels)"
  [ "$result" = "integration,smoke,contract,hostile" ]
}

@test "ptr_map_label applies labelAliases then labelPrefix" {
  lang_json='{"labelPrefix":"type:","labelAliases":{"contract":"contracts"}}'
  result="$(ptr_map_label "$lang_json" "contract")"
  [ "$result" = "type:contracts" ]
  result="$(ptr_map_label "$lang_json" "integration")"
  [ "$result" = "type:integration" ]
}

# --- ptr_validate_config -------------------------------------------------

@test "ptr_validate_config rejects invalid JSON with exit 13" {
  echo '{not json' >"$TEST_DIR/bad.json"
  run ptr_validate_config "$TEST_DIR/bad.json"
  [ "$status" -eq 13 ]
}

@test "ptr_validate_config rejects an unsupported version with exit 13" {
  echo '{"version":2,"languages":[]}' >"$TEST_DIR/badversion.json"
  run ptr_validate_config "$TEST_DIR/badversion.json"
  [ "$status" -eq 13 ]
}

@test "ptr_validate_config rejects an unknown placeholder with exit 13" {
  echo '{"version":1,"languages":[{"name":"x","markers":["m"],"tools":[],"run":{"unit":["echo","{bogus}"]}}]}' >"$TEST_DIR/badplaceholder.json"
  run ptr_validate_config "$TEST_DIR/badplaceholder.json"
  [ "$status" -eq 13 ]
}

@test "ptr_validate_config accepts the well-formed fixture config" {
  run ptr_validate_config "$CONFIG_PATH"
  [ "$status" -eq 0 ]
}
