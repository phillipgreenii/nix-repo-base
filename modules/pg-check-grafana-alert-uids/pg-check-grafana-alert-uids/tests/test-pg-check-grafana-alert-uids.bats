#!/usr/bin/env bats
# bats file_tags=type:unit
# shellcheck disable=SC2030,SC2031  # per-test state

bats_require_minimum_version 1.5.0

# Hermetic: every test writes its own alerting file under a fresh temp dir and
# runs the REAL script against the REAL yq (yq-go); nothing outside that dir is
# read or written. The shapes (groups[].rules[].uid, contactPoints[].receivers[].uid)
# are the ones the real alert files use (nix-agent-support packages/*/grafana/alerting).

setup() {
  TEST_DIR="$(mktemp -d)"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SUT="$SCRIPT_UNDER_TEST"
  else
    # Raw source: the script has no library, so the builder's preamble is all it
    # is missing.
    SUT="$TEST_DIR/pg-check-grafana-alert-uids"
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "${BATS_TEST_DIRNAME}/../pg-check-grafana-alert-uids.sh"
    } >"$SUT"
    chmod +x "$SUT"
  fi

  # uid_of N: a uid of exactly N characters.
  uid_of() {
    local n=$1 s=""
    while ((${#s} < n)); do s="${s}a"; done
    printf '%s' "$s"
  }
  UID40="$(uid_of 40)"
  UID41="$(uid_of 41)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# rules_file <name> <uid>...: a rules file with one rule per uid.
rules_file() {
  local f="$TEST_DIR/$1" uid
  shift
  {
    printf 'apiVersion: 1\ngroups:\n  - orgId: 1\n    name: g\n    folder: f\n    interval: 1m\n    rules:\n'
    for uid in "$@"; do
      printf '      - uid: %s\n        title: t\n        condition: A\n' "$uid"
    done
  } >"$f"
  printf '%s' "$f"
}

@test "--help prints usage and exits 0" {
  run "$SUT" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage:"* ]]
}

@test "no file is a usage error with exit 2" {
  run "$SUT"
  [ "$status" -eq 2 ]
}

@test "an unknown option is a usage error with exit 2" {
  run "$SUT" --nope
  [ "$status" -eq 2 ]
}

@test "a 40-character rule uid passes" {
  f="$(rules_file ok.yaml "$UID40")"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
}

@test "a 41-character rule uid is rejected, naming file, uid and length" {
  f="$(rules_file long.yaml "short-one" "$UID41")"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"$f"* ]]
  [[ $stderr == *"alert-rule uid '$UID41' is 41 bytes long"* ]]
  # the short uid is not reported
  [[ $stderr != *"'short-one'"* ]]
}

@test "a duplicate rule uid within a file is rejected" {
  f="$(rules_file dup.yaml one two one)"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"$f"* ]]
  [[ $stderr == *"alert-rule uid 'one'"*"more than once"* ]]
  [[ $stderr != *"uid 'two'"* ]]
}

@test "the same rule uid in two different files is not a per-file duplicate" {
  a="$(rules_file a.yaml shared)"
  b="$(rules_file b.yaml shared)"
  run --separate-stderr "$SUT" "$a" "$b"
  [ "$status" -eq 0 ]
}

@test "a rule uid repeated across two groups of one file is rejected" {
  f="$TEST_DIR/groups.yaml"
  cat >"$f" <<'YAML'
apiVersion: 1
groups:
  - orgId: 1
    name: g1
    rules:
      - uid: same
  - orgId: 1
    name: g2
    rules:
      - uid: same
YAML
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"alert-rule uid 'same'"*"more than once"* ]]
}

@test "a 41-character receiver uid is rejected, naming file, uid and length" {
  f="$TEST_DIR/cp.yaml"
  cat >"$f" <<YAML
apiVersion: 1
contactPoints:
  - orgId: 1
    name: local-desktop
    receivers:
      - uid: $UID41
        type: webhook
YAML
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"$f"* ]]
  [[ $stderr == *"contact-point receiver uid '$UID41' is 41 bytes long"* ]]
}

@test "a 40-character receiver uid passes" {
  f="$TEST_DIR/cp-ok.yaml"
  cat >"$f" <<YAML
apiVersion: 1
contactPoints:
  - orgId: 1
    name: local-desktop
    receivers:
      - uid: $UID40
        type: webhook
YAML
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 0 ]
}

@test "a repeated receiver uid is not rejected (only the length is capped)" {
  f="$TEST_DIR/cp-dup.yaml"
  cat >"$f" <<'YAML'
apiVersion: 1
contactPoints:
  - orgId: 1
    name: a
    receivers:
      - uid: same
  - orgId: 1
    name: b
    receivers:
      - uid: same
YAML
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 0 ]
}

@test "a long deleteRules uid is not a rule definition and passes" {
  f="$TEST_DIR/del.yaml"
  cat >"$f" <<YAML
apiVersion: 1
deleteRules:
  - orgId: 1
    uid: $UID41
groups: []
YAML
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 0 ]
}

@test "a file with neither groups nor contactPoints passes" {
  f="$TEST_DIR/empty.yaml"
  printf 'apiVersion: 1\n' >"$f"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 0 ]
}

@test "a file that is not valid YAML is rejected" {
  f="$TEST_DIR/bad.yaml"
  printf 'groups: [unclosed\n' >"$f"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"$f"*"cannot parse"* ]]
}

@test "every violation across several files is reported in one run" {
  good="$(rules_file good.yaml fine)"
  long="$(rules_file long.yaml "$UID41")"
  dup="$(rules_file dup.yaml x x)"
  run --separate-stderr "$SUT" "$good" "$long" "$dup"
  [ "$status" -eq 1 ]
  [[ $stderr == *"$long"* ]]
  [[ $stderr == *"$dup"* ]]
  [[ $stderr != *"$good"* ]]
}

@test "a rule uid of 21 two-byte characters (42 bytes) is over the byte limit and rejected" {
  # 21 two-byte characters = 42 bytes but only 21 characters: Grafana counts bytes.
  multi="$(printf 'é%.0s' $(seq 1 21))"
  f="$TEST_DIR/multi.yaml"
  printf 'apiVersion: 1\ngroups:\n  - rules:\n      - uid: %s\n' "$multi" >"$f"
  run --separate-stderr "$SUT" "$f"
  [ "$status" -eq 1 ]
  [[ $stderr == *"is 42 bytes long"* ]]
}
