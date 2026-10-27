# shellcheck shell=bash
# pg-tempo-search: Tempo /api/search that ALWAYS sets start and end.
#
# Why (bead pg2-sdbhh4.15): a Tempo /api/search call WITHOUT start and end
# silently searches only the ingesters' recent data (about the last 15
# minutes). On 2026-10-26 that returned 2 wrapper traces where the same query
# with explicit windows returns about 2,000 per 7 days, and an analysis
# wrongly concluded that pn traces were missing. This helper makes the
# explicit window impossible to forget, slices a long lookback into windows of
# at most 24 h, splits any slice that hits the result limit (Tempo truncates
# silently at the limit), and merges everything into one JSON array.

DAY_SECONDS=86400
MIN_SLICE_SECONDS=3600
MAX_LOOKBACK_SECONDS=$((168 * 3600))
EXIT_TRUNCATED=3

show_help() {
  cat <<'HELP'
pg-tempo-search: search Tempo with an explicit time window, always

Usage: pg-tempo-search [OPTIONS] <traceql-query>

Runs the TraceQL query against Tempo's /api/search over the last LOOKBACK,
and prints ONE JSON array of the matching traces on stdout. Every request
carries start and end. Without them Tempo silently searches only recent
ingester data (about the last 15 minutes) and returns a near-empty answer.

The range is cut into slices of at most 24 h (limit 1000 traces per slice).
A slice that returns exactly the limit may be truncated, so it is split in
half and re-queried, recursively, until each half is no shorter than 1 h. A
slice of under 2 h still at the limit is reported as truncated (see exit 3).
A trace seen in two slices appears once, with its spans merged.

Options:
  --lookback DUR   How far back to search: <N>d, <N>h or <N>m. Default 7d.
                   At most 168h (Tempo rejects wider ranges).
  --select LIST    Attributes to return per span, e.g.
                   'span.nix.verb, span.pn.verb'. Appended to the query as
                   ' | select(LIST)'. Do NOT select span:startTime or
                   span:duration (Tempo 2.10.5 rejects them); every span
                   already carries startTimeUnixNano and durationNanos.
  --limit N        Traces per slice (default 1000).
  -h, --help       Show this help message
  -v, --version    Show version information

Output: a JSON array. Each element is the trace object Tempo returned
(traceID, rootServiceName, rootTraceName, startTimeUnixNano, durationMs),
plus "spans": the matched spans, each {spanID, startTimeUnixNano,
durationNanos (a number; Tempo sends a string, and the field is NOT named
durationNs), <selected attribute>: <plain value>, ...}.

Environment:
  PG_TEMPO_URL     Tempo base URL. Default http://127.0.0.1:3200.

Exit codes:
  0   success
  1   Tempo unreachable, an HTTP error, or an unparseable response
  2   usage error (including a lookback over 168h)
  3   success, but a 1 h slice still hit the limit: its results may be
      truncated. The array is still printed; a warning on stderr names the window.
HELP
}

say() {
  printf 'pg-tempo-search: %s\n' "$*" >&2
}

die() {
  say "error: $1"
  exit "${2:-1}"
}

die_usage() {
  die "$1 (see --help)" 2
}

# fmt_ts EPOCH: UTC ISO-8601 (GNU date, with a BSD date fallback for local runs).
fmt_ts() {
  date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null ||
    date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ
}

# parse_lookback DUR: print the duration in seconds, or exit 2.
parse_lookback() {
  local dur=$1 n unit
  if [[ ! $dur =~ ^([0-9]+)([dhm])$ ]]; then
    die_usage "invalid --lookback '$dur': expected <N>d, <N>h or <N>m, for example 7d"
  fi
  n=$((10#${BASH_REMATCH[1]}))
  unit=${BASH_REMATCH[2]}
  case "$unit" in
  d) printf '%s\n' $((n * DAY_SECONDS)) ;;
  h) printf '%s\n' $((n * 3600)) ;;
  m) printf '%s\n' $((n * 60)) ;;
  esac
}

# search_window START END: one request carrying start and end. A window that
# returns exactly LIMIT traces may be truncated: split it in half and recurse
# while each half stays at least MIN_SLICE_SECONDS; otherwise warn and flag it.
# Only leaf windows contribute traces (a split window's own body is dropped).
search_window() {
  local start=$1 end=$2 body count mid
  if ! body=$(curl -fsS --max-time 120 -G "$BASE_URL/api/search" \
    --data-urlencode "q=$QUERY" \
    --data-urlencode "start=$start" \
    --data-urlencode "end=$end" \
    --data-urlencode "limit=$LIMIT" \
    --data-urlencode "spss=100"); then
    die "request to $BASE_URL/api/search failed for window $(fmt_ts "$start") .. $(fmt_ts "$end")"
  fi
  if ! count=$(jq -e '(.traces // []) | length' <<<"$body"); then
    die "unparseable response from $BASE_URL/api/search for window $(fmt_ts "$start") .. $(fmt_ts "$end")"
  fi

  if ((count >= LIMIT)); then
    if ((end - start >= 2 * MIN_SLICE_SECONDS)); then
      mid=$((start + (end - start) / 2))
      search_window "$start" "$mid"
      search_window "$mid" "$end"
      return 0
    fi
    say "warning: window $(fmt_ts "$start") .. $(fmt_ts "$end") returned $count traces, the limit ($LIMIT), and cannot be split below 1 h: its results may be truncated"
    TRUNCATED=1
  fi
  jq -c '(.traces // [])[]' <<<"$body" >>"$RESULTS"
}

# Flatten each trace's matched spans, then merge traces seen in more than one
# slice (by traceID, unioning spans by spanID) and order by start time.
MERGE_JQ='
def val:
  if .stringValue != null then .stringValue
  elif .intValue != null then (.intValue | tonumber)
  elif .doubleValue != null then .doubleValue
  elif .boolValue != null then .boolValue
  else null end;
def flat_span:
  (del(.attributes)
   | if has("durationNanos") then .durationNanos |= tonumber else . end)
  + ((.attributes // []) | map({(.key): (.value | val)}) | add // {});
def trace_spans:
  [(.spanSets // (if .spanSet then [.spanSet] else [] end))[] | (.spans // [])[] | flat_span];
map(del(.spanSet, .spanSets) + {spans: trace_spans})
| group_by(.traceID)
| map(.[0] + {spans: (map(.spans) | add | unique_by(.spanID))})
| sort_by((.startTimeUnixNano // "0") | tonumber)
'

LOOKBACK=7d
SELECT=""
LIMIT=1000
QUERY=""
HAVE_QUERY=0
while (($# > 0)); do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --lookback | --select | --limit)
    (($# >= 2)) || die_usage "$1 needs a value"
    case "$1" in
    --lookback) LOOKBACK=$2 ;;
    --select) SELECT=$2 ;;
    --limit) LIMIT=$2 ;;
    esac
    shift 2
    ;;
  --)
    shift
    break
    ;;
  -*)
    die_usage "unknown option: $1"
    ;;
  *)
    ((HAVE_QUERY == 0)) || die_usage "expected exactly one TraceQL query"
    QUERY=$1
    HAVE_QUERY=1
    shift
    ;;
  esac
done
if (($# > 0)); then
  ((HAVE_QUERY == 0 && $# == 1)) || die_usage "expected exactly one TraceQL query"
  QUERY=$1
  HAVE_QUERY=1
fi
((HAVE_QUERY == 1)) || die_usage "missing <traceql-query>"
[[ $LIMIT =~ ^[1-9][0-9]*$ ]] || die_usage "invalid --limit '$LIMIT': expected a positive integer"

LOOKBACK_SECONDS=$(parse_lookback "$LOOKBACK")
if ((LOOKBACK_SECONDS == 0)); then
  die_usage "--lookback must be greater than zero"
fi
if ((LOOKBACK_SECONDS > MAX_LOOKBACK_SECONDS)); then
  die_usage "--lookback $LOOKBACK exceeds the 168h (7d) maximum: Tempo rejects wider search ranges; use 168h or less, or run several searches"
fi

if [[ -n $SELECT ]]; then
  QUERY="$QUERY | select($SELECT)"
fi

BASE_URL=${PG_TEMPO_URL:-http://127.0.0.1:3200}
BASE_URL=${BASE_URL%/}
# PG_TEMPO_NOW (epoch seconds) pins "now" so tests are deterministic.
NOW=${PG_TEMPO_NOW:-$(date +%s)}
RANGE_START=$((NOW - LOOKBACK_SECONDS))
TRUNCATED=0

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
RESULTS="$WORK/traces.ndjson"
: >"$RESULTS"

slice_start=$RANGE_START
while ((slice_start < NOW)); do
  slice_end=$((slice_start + DAY_SECONDS))
  ((slice_end <= NOW)) || slice_end=$NOW
  search_window "$slice_start" "$slice_end"
  slice_start=$slice_end
done

jq -s "$MERGE_JQ" "$RESULTS"

if ((TRUNCATED == 1)); then
  exit "$EXIT_TRUNCATED"
fi
