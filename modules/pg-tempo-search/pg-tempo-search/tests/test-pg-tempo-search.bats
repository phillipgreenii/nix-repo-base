#!/usr/bin/env bats
# bats file_tags=type:unit
#
# CLI suite for pg-tempo-search, driving the ASSEMBLED artifact via
# SCRIPT_UNDER_TEST (in the nix check that is the real wrapped binary; for a
# local `bats tests/` run setup() assembles an equivalent wrapper sourcing
# pg-tempo-search.sh). Tempo is a LOOPBACK STUB (tests/stub-tempo.py): no live
# Tempo and no outside host is contacted. The stub answers 400 to a request
# without start and end, serves the traces of a per-test dataset that overlap
# the requested window (at most `limit` of them), and logs every query.
#
# "Now" is pinned with PG_TEMPO_NOW to NOW, a multiple of 86400, so slice
# boundaries are exact.

bats_require_minimum_version 1.5.0

NOW=1799971200
DAY=86400

setup_file() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  fi
  export SCRIPTS_DIR
}

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    cat >"$TEST_DIR/pg-tempo-search-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$SCRIPTS_DIR/pg-tempo-search.sh"
WRAPPER
    chmod +x "$TEST_DIR/pg-tempo-search-wrapper"
    SCRIPT="$TEST_DIR/pg-tempo-search-wrapper"
  fi
  export SCRIPT
  export HOME="$TEST_DIR"
  export PG_TEMPO_NOW="$NOW"
  REQ_LOG="$TEST_DIR/requests.jsonl"
  : >"$REQ_LOG"
  STUB_PID=""
}

teardown() {
  if [[ -n $STUB_PID ]]; then
    kill "$STUB_PID" 2>/dev/null || true
    wait "$STUB_PID" 2>/dev/null || true
  fi
  rm -rf "$TEST_DIR"
}

# start_stub DATASET_JSON: serve the dataset on a loopback port and point
# PG_TEMPO_URL at it.
start_stub() {
  printf '%s' "$1" >"$TEST_DIR/dataset.json"
  python3 "$BATS_TEST_DIRNAME/stub-tempo.py" "$TEST_DIR/dataset.json" "$TEST_DIR/port" "$REQ_LOG" &
  STUB_PID=$!
  local _
  for _ in $(seq 1 100); do
    [[ -s $TEST_DIR/port ]] && break
    sleep 0.1
  done
  [[ -s $TEST_DIR/port ]] || {
    echo "stub server did not start" >&2
    return 1
  }
  PG_TEMPO_URL="http://127.0.0.1:$(cat "$TEST_DIR/port")"
  export PG_TEMPO_URL
}

# trace ID START END [SPAN_JSON...]: one dataset trace (default: one span).
trace() {
  local id=$1 start=$2 end=$3
  shift 3
  local spans
  if (($# > 0)); then
    spans=$(printf '%s\n' "$@" | jq -s '.')
  else
    spans="[{\"spanID\":\"${id}-s1\",\"durationNanos\":1000}]"
  fi
  jq -nc --arg id "$id" --argjson s "$start" --argjson e "$end" --argjson spans "$spans" \
    '{traceID: $id, start: $s, end: $e, spans: $spans}'
}

dataset() {
  jq -sc '.'
}

fmt_ts() {
  date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ
}

request_count() {
  wc -l <"$REQ_LOG" | tr -d ' '
}

@test "--help documents the start/end trap and exits 0" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ $output == *"start and end"* ]]
  [[ $output == *"168h"* ]]
  [[ $output == *"durationNanos"* ]]
}

@test "every request carries start, end, limit and the query" {
  start_stub "$(trace t1 $((NOW - 100)) $((NOW - 50)) | dataset)"
  run "$SCRIPT" '{ name = "x" }'
  [ "$status" -eq 0 ]
  [ "$(request_count)" -eq 7 ]
  run jq -s 'all(.[]; (.query | has("start") and has("end") and has("limit")) and .query.q == "{ name = \"x\" }" and .path == "/api/search")' "$REQ_LOG"
  [ "$output" = "true" ]
}

@test "a 7-day lookback is 7 contiguous requests of at most 24h covering the range" {
  start_stub '[]'
  run "$SCRIPT" '{ name = "x" }'
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
  [ "$(request_count)" -eq 7 ]
  run jq -s --argjson now "$NOW" --argjson day "$DAY" '
    [.[].query | {s: (.start | tonumber), e: (.end | tonumber)}] as $w
    | all($w[]; (.e - .s) <= $day and (.e - .s) > 0)
      and $w[0].s == ($now - 7 * $day)
      and $w[-1].e == $now
      and ([range(1; $w | length)] | all(. as $i | $w[$i].s == $w[$i - 1].e))' "$REQ_LOG"
  [ "$output" = "true" ]
}

@test "a lookback over 168h is rejected with a clear message and no request" {
  start_stub '[]'
  run "$SCRIPT" --lookback 169h '{ name = "x" }'
  [ "$status" -eq 2 ]
  [[ $output == *"exceeds the 168h"* ]]
  run "$SCRIPT" --lookback 8d '{ name = "x" }'
  [ "$status" -eq 2 ]
  [ "$(request_count)" -eq 0 ]
}

@test "a lookback of exactly 168h is accepted" {
  start_stub '[]'
  run "$SCRIPT" --lookback 168h '{ name = "x" }'
  [ "$status" -eq 0 ]
  [ "$(request_count)" -eq 7 ]
}

@test "malformed lookback, missing query and unknown options are usage errors" {
  run "$SCRIPT" --lookback 7 '{ name = "x" }'
  [ "$status" -eq 2 ]
  run "$SCRIPT"
  [ "$status" -eq 2 ]
  [[ $output == *"missing <traceql-query>"* ]]
  run "$SCRIPT" --bogus '{ name = "x" }'
  [ "$status" -eq 2 ]
  run "$SCRIPT" --limit 0 '{ name = "x" }'
  [ "$status" -eq 2 ]
}

@test "a slice returning exactly the limit is split in half and re-queried" {
  # Three traces in the one day: at limit 3 the day slice is truncated-looking.
  start_stub "$(
    {
      trace a $((NOW - 80000)) $((NOW - 79990))
      trace b $((NOW - 70000)) $((NOW - 69990))
      trace c $((NOW - 30000)) $((NOW - 29990))
    } | dataset
  )"
  run "$SCRIPT" --lookback 24h --limit 3 '{ name = "x" }'
  [ "$status" -eq 0 ]
  [ "$(request_count)" -eq 3 ]
  run jq -s --argjson now "$NOW" --argjson day "$DAY" '
    [.[].query | (.end | tonumber) - (.start | tonumber)] == [$day, $day / 2, $day / 2]' "$REQ_LOG"
  [ "$output" = "true" ]
  # The halves hold 2 and 1 traces: all three come back, once each.
  run jq -r '[.[].traceID] | sort | join(",")' <<<"$("$SCRIPT" --lookback 24h --limit 3 '{ name = "x" }')"
  [ "$output" = "a,b,c" ]
}

@test "a 1h slice still at the limit warns with the window and exits 3, after printing results" {
  start_stub "$(
    {
      trace a $((NOW - 3000)) $((NOW - 2990))
      trace b $((NOW - 2000)) $((NOW - 1990))
      trace c $((NOW - 1000)) $((NOW - 990))
    } | dataset
  )"
  run --separate-stderr "$SCRIPT" --lookback 1h --limit 2 '{ name = "x" }'
  [ "$status" -eq 3 ]
  [ "$(request_count)" -eq 1 ]
  [[ $stderr == *"warning: window $(fmt_ts $((NOW - 3600))) .. $(fmt_ts "$NOW") returned 2 traces, the limit (2)"* ]]
  [ "$(jq 'length' <<<"$output")" -eq 2 ]
}

@test "splitting recurses down to 1h and never queries a narrower window" {
  start_stub "$(trace a $((NOW - 100)) $((NOW - 90)) | dataset)"
  run --separate-stderr "$SCRIPT" --lookback 4h --limit 1 '{ name = "x" }'
  [ "$status" -eq 3 ]
  # 4h -> two 2h; the half holding the trace -> two 1h (the 1h one still at limit).
  [ "$(request_count)" -eq 5 ]
  run jq -s '[.[].query | (.end | tonumber) - (.start | tonumber)] | min' "$REQ_LOG"
  [ "$output" -eq 3600 ]
}

@test "traces are merged and deduplicated by traceID across slices" {
  boundary=$((NOW - DAY))
  # "edge" straddles the boundary between the last two daily slices, so both
  # slices return it; its two spans must appear once each, not twice.
  start_stub "$(
    {
      trace edge $((boundary - 100)) $((boundary + 100)) \
        '{"spanID":"e1","durationNanos":5}' '{"spanID":"e2","durationNanos":6}'
      trace other $((NOW - 500)) $((NOW - 400))
    } | dataset
  )"
  run "$SCRIPT" '{ name = "x" }'
  [ "$status" -eq 0 ]
  [ "$(jq 'length' <<<"$output")" -eq 2 ]
  [ "$(jq '[.[] | select(.traceID == "edge")] | length' <<<"$output")" -eq 1 ]
  [ "$(jq -r '.[] | select(.traceID == "edge") | [.spans[].spanID] | sort | join(",")' <<<"$output")" = "e1,e2" ]
}

@test "--select is appended to the query and attributes are flattened onto the spans" {
  start_stub "$(
    trace t1 $((NOW - 100)) $((NOW - 50)) \
      '{"spanID":"s1","durationNanos":123456789012,"attrs":{"nix.verb":"build","pn.exit":3}}' | dataset
  )"
  run "$SCRIPT" --lookback 1d --select 'span.nix.verb, span.pn.exit' '{ name = "x" }'
  [ "$status" -eq 0 ]
  run jq -s -r '.[0].query.q' "$REQ_LOG"
  [ "$output" = '{ name = "x" } | select(span.nix.verb, span.pn.exit)' ]
  out=$("$SCRIPT" --lookback 1d --select 'span.nix.verb, span.pn.exit' '{ name = "x" }')
  [ "$(jq -r '.[0].traceID' <<<"$out")" = "t1" ]
  [ "$(jq -r '.[0].rootServiceName' <<<"$out")" = "stub" ]
  [ "$(jq -r '.[0].spans[0]["nix.verb"]' <<<"$out")" = "build" ]
  # intValue strings become numbers; durationNanos becomes a number.
  [ "$(jq -r '.[0].spans[0]["pn.exit"] | type' <<<"$out")" = "number" ]
  [ "$(jq -r '.[0].spans[0].durationNanos | type' <<<"$out")" = "number" ]
  [ "$(jq -r '.[0].spans[0].durationNanos' <<<"$out")" = "123456789012" ]
  [ "$(jq -r '.[0].spans[0] | has("attributes")' <<<"$out")" = "false" ]
  [ "$(jq -r '.[0] | has("spanSet")' <<<"$out")" = "false" ]
}

@test "an unreachable Tempo is a non-zero error that names the window" {
  PG_TEMPO_URL="http://127.0.0.1:1"
  export PG_TEMPO_URL
  run "$SCRIPT" --lookback 1h '{ name = "x" }'
  [ "$status" -eq 1 ]
  [[ $output == *"request to http://127.0.0.1:1/api/search failed"* ]]
}
