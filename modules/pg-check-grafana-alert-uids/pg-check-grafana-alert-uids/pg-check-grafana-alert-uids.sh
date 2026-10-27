# shellcheck shell=bash

# pg-check-grafana-alert-uids: commit-time guard for Grafana alerting
# provisioning files (bead pg2-hlxmkk).
#
# Grafana rejects any alert-rule uid longer than 40 characters and then fails to
# START when provisioning hits one (2026-10-13: a 42-character pg-desk uid
# crash-looped Grafana and aborted pn workspace apply). A duplicated alert-rule
# uid is rejected the same way. nix-support-apps' build-time funnel validator
# (darwin/modules/observability/alerting-validate.nix) catches both only when the
# whole system is built; this hook catches them in the repo that defines the
# alert, at commit time. The queries below are the funnel validator's, so the two
# agree on what a violation is.
#
# The YAML is read with a real parser (yq-go), never a regex.
# BASH 3.2 STYLE (no mapfile, no associative arrays) like the rest of the hooks.

show_help() {
  cat <<'HELP'
pg-check-grafana-alert-uids: reject Grafana alerting files with a bad uid

Usage: pg-check-grafana-alert-uids FILE...

For every FILE (a Grafana alerting provisioning YAML) it fails when:
  - an alert-rule uid (.groups[].rules[].uid) is longer than 40 characters;
  - an alert-rule uid is declared twice within the same file;
  - a contact-point receiver uid (.contactPoints[].receivers[].uid) is longer
    than 40 characters;
  - the file cannot be parsed as YAML.

Each violation is printed on stderr with the file, the uid and its length.
Length is counted in bytes, which is how Grafana counts it.

Meant to run as the check-grafana-alert-uids prek hook over
grafana/alerting/*.yaml (rule-test files under rule-tests/ excluded).

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Exit codes:
  0  every uid is within the limit and no rule uid repeats
  1  a violation was found, or a file could not be parsed
  2  usage error (no FILE, unknown option)
HELP
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "pg-check-grafana-alert-uids: unknown option: $1" >&2
    exit 2
    ;;
  *) break ;;
  esac
done

if [[ $# -eq 0 ]]; then
  echo "pg-check-grafana-alert-uids: no FILE given" >&2
  show_help >&2
  exit 2
fi

# Bytes, not locale characters: Grafana's limit is on the raw uid string.
export LC_ALL=C
max=40

rules_query='(.groups // [])[] | (.rules // [])[] | .uid | select(. != null)'
receivers_query='(.contactPoints // [])[] | (.receivers // [])[] | .uid | select(. != null)'

status=0

# check_length FILE KIND UIDS: print one line per uid over the cap.
check_length() {
  local file=$1 kind=$2 uids=$3 uid
  while IFS= read -r uid; do
    [[ -n $uid ]] || continue
    if ((${#uid} > max)); then
      echo "ERROR: $file: $kind uid '$uid' is ${#uid} bytes long (limit $max); Grafana rejects uids longer than $max and fails to start" >&2
      status=1
    fi
  done <<<"$uids"
}

for file in "$@"; do
  if ! rule_uids="$(yq "$rules_query" "$file" 2>&1)"; then
    echo "ERROR: $file: cannot parse as Grafana alerting provisioning YAML: $rule_uids" >&2
    status=1
    continue
  fi
  if ! receiver_uids="$(yq "$receivers_query" "$file" 2>&1)"; then
    echo "ERROR: $file: cannot parse as Grafana alerting provisioning YAML: $receiver_uids" >&2
    status=1
    continue
  fi

  check_length "$file" alert-rule "$rule_uids"
  check_length "$file" "contact-point receiver" "$receiver_uids"

  if [[ -n $rule_uids ]]; then
    duplicates="$(printf '%s\n' "$rule_uids" | sort | uniq -d)"
    while IFS= read -r uid; do
      [[ -n $uid ]] || continue
      echo "ERROR: $file: alert-rule uid '$uid' (${#uid} bytes) is declared more than once in this file; Grafana rejects duplicate alert-rule uids" >&2
      status=1
    done <<<"$duplicates"
  fi
done

exit "$status"
