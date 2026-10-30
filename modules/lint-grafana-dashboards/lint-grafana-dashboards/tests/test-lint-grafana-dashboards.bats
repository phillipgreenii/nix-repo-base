#!/usr/bin/env bats
# bats file_tags=type:unit

bats_require_minimum_version 1.5.0

# Hermetic: runs the REAL script against the REAL jq and rg over the checked-in
# fixtures (read-only). The fixtures are the negative controls: each l*.json
# trips exactly the rule in its name.

setup() {
  TEST_DIR="$(mktemp -d)"
  FIX="${BATS_TEST_DIRNAME}/fixtures"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SUT="$SCRIPT_UNDER_TEST"
  else
    # Raw source: add the builder's preamble and the injected config.
    SUT="$TEST_DIR/lint-grafana-dashboards"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      printf 'PUSHED_GAUGES_FILE=%q\n' "${BATS_TEST_DIRNAME}/../pushed-gauges.txt"
      cat "${BATS_TEST_DIRNAME}/../lint-grafana-dashboards.sh"
    } >"$SUT"
    chmod +x "$SUT"
  fi
}

teardown() {
  rm -rf "$TEST_DIR"
}

# only_rule FIXTURE RULE: fails with exactly RULE (and no other RULE-Ln line).
only_rule() {
  run "$SUT" "$FIX/$1"
  [ "$status" -eq 1 ]
  [[ $output == *"RULE-$2: "* ]]
  local other
  for other in L1 L2 L3 L4 L5; do
    [[ $other == "$2" ]] && continue
    [[ $output != *"RULE-$other: "* ]]
  done
}

@test "clean.json passes silently" {
  run "$SUT" "$FIX/clean.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "l1-opted-out.json passes silently" {
  run "$SUT" "$FIX/l1-opted-out.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "l1-last-over-time.json passes silently" {
  run "$SUT" "$FIX/l1-last-over-time.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "L1 without --error exits 0 with a WARN" {
  run "$SUT" "$FIX/l1-fixed-window.json"
  [ "$status" -eq 0 ]
  [[ $output == *"WARN L1: 1 targets with fixed windows"* ]]
  [[ $output != *"RULE-L1"* ]]
}

@test "L1 with --error L1 fails with RULE-L1" {
  run "$SUT" --error L1 "$FIX/l1-fixed-window.json"
  [ "$status" -eq 1 ]
  [[ $output == *"RULE-L1: "*"l1-fixed-window.json: Fixed window panel"* ]]
}

@test "--error L1 still passes the opted-out and last_over_time fixtures" {
  run "$SUT" --error L1 "$FIX/l1-opted-out.json" "$FIX/l1-last-over-time.json" "$FIX/clean.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "--list L1 prints file | id | title | expr for the unmarked window" {
  run "$SUT" --list L1 "$FIX/l1-fixed-window.json"
  [ "$status" -eq 0 ]
  [ "$output" = "$FIX/l1-fixed-window.json | 7 | Fixed window panel | rate(requests_total[5m])" ]
}

@test "--list L1 prints nothing for clean, opted-out and last_over_time" {
  run "$SUT" --list L1 "$FIX/clean.json" "$FIX/l1-opted-out.json" "$FIX/l1-last-over-time.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "l2-label.json fails with exactly RULE-L2" {
  only_rule l2-label.json L2
}

@test "l2-sum-pushed-gauge.json fails with exactly RULE-L2" {
  only_rule l2-sum-pushed-gauge.json L2
}

@test "l2-bare-pushed-gauge.json fails with exactly RULE-L2" {
  only_rule l2-bare-pushed-gauge.json L2
}

@test "l3-duplicate-title.json fails with exactly RULE-L3" {
  only_rule l3-duplicate-title.json L3
  [[ $output == *"Same title"* ]]
}

@test "l3-row-var.json fails with exactly RULE-L3" {
  only_rule l3-row-var.json L3
  [[ $output == *"Host \$host"* ]]
}

@test "l4-empty-title.json fails with exactly RULE-L4" {
  only_rule l4-empty-title.json L4
}

@test "l5-stat-range.json fails with exactly RULE-L5" {
  only_rule l5-stat-range.json L5
}

@test "pushed-gauges.txt holds full metric names only" {
  local gauges="${PUSHED_GAUGES_FILE:-${BATS_TEST_DIRNAME}/../pushed-gauges.txt}"
  run rg -v '^(#.*|[a-z][a-z0-9_]*)$' "$gauges"
  [ "$status" -eq 1 ]
  run rg -c '^pg_connector_github_budget_remaining$' "$gauges"
  [ "$status" -eq 0 ]
}

@test "invalid JSON fails" {
  printf 'not json' >"$TEST_DIR/bad.json"
  run "$SUT" "$TEST_DIR/bad.json"
  [ "$status" -eq 1 ]
  [[ $output == *"not valid dashboard JSON"* ]]
}

@test "no files is a usage error" {
  run "$SUT"
  [ "$status" -eq 2 ]
}
