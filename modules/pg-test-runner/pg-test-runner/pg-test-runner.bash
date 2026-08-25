# shellcheck shell=bash
# pg-test-runner core library — data-driven discovery + execution engine.
# See docs/superpowers/specs/2026-08-24-pg-test-runner-design.md (rev 6),
# section 2, for the normative behavior this file implements.
#
# Every function here is a plain function operating on globals populated by
# ptr_run (or, in unit tests, by the test itself) — there is no object model,
# just a small set of arrays/strings threaded through. jq does all JSON
# reading; bash never hand-parses JSON.
#
# Engine-internal tool resolution (jq, timeout) is DELIBERATELY separate from
# the language `tools` gate (section 2.5): these two vars resolve via an
# ABSOLUTE nix store path when the assembled wrapper injects them (config
# injection, see default.nix), falling back to a bare PATH lookup for raw
# source / test runs. This is NOT the same mechanism as the `tools` list
# check below, which MUST see only the ambient PATH so a missing language
# tool fails loudly rather than being silently satisfied by pg-test-runner's
# own bundled fallback.
#
# Default here for a direct-library unit test, which sources this file and
# calls its functions WITHOUT ever loading pg-test-runner.sh (so no config
# injection could apply anyway). The REAL resolution -- reading
# PG_TEST_RUNNER_JQ_BIN/PG_TEST_RUNNER_TIMEOUT_BIN -- deliberately lives in
# pg-test-runner.sh, not here: mkBashScript composes the assembled script as
# `libraries -> support .bash (sourced) -> config lines -> .sh body
# (INLINED, not sourced)`, so a config-injected var is genuinely unreadable
# from this file at the point it is sourced. Worse, a read placed in a
# FUNCTION here would still be textually inside a followed `source`, which
# is exactly the shape shellcheck's unused-variable analysis fails to
# recognize as a use of the config line -- so the assembled artifact's own
# lint pass caught this twice (first as a file-scope ordering bug, then
# again as a same shape hiding inside a function) before the read was moved
# to the one place shellcheck treats as the same textual unit as the config
# lines: the inlined .sh body.
PTR_JQ="jq"
PTR_TIMEOUT="timeout"

ptr_err() {
  printf 'pg-test-runner: %s\n' "$1" >&2
}

ptr_die() {
  # $1: exit code, $2: message
  ptr_err "$2"
  exit "$1"
}

# ---------------------------------------------------------------------------
# Config loading + validation (section 2.1, 2.6 exit 13)
# ---------------------------------------------------------------------------

# Validate the config at $1: readable, valid JSON, version == 1, and every
# argv template uses only the closed placeholder set (section 2.1). Exits 13
# on any failure; MUST be called before any project discovery/execution so
# the check happens at "startup" regardless of which languages get used.
ptr_validate_config() {
  local path="$1"
  [[ -n $path && -r $path ]] || ptr_die 13 "config not found or unreadable: '$path'"
  "$PTR_JQ" empty "$path" >/dev/null 2>&1 || ptr_die 13 "config is not valid JSON: $path"

  local version
  version="$("$PTR_JQ" -r '.version // empty' "$path" 2>/dev/null)"
  [[ $version == "1" ]] || ptr_die 13 "config has unsupported version '$version' (expected 1): $path"

  local known=(jobs labels label unitExclusion allLabels)
  local tokens
  tokens="$("$PTR_JQ" -r '
      [ .languages[]? |
        (.run.unit // []), (.run.labels // []), (.run.all // []),
        (.run.probe.unit // []), (.run.probe.labels // []), (.run.probe.all // [])
      ] | flatten | .[]?
    ' "$path" 2>/dev/null)"

  local line placeholder ok k
  while IFS= read -r line; do
    [[ -z $line ]] && continue
    while [[ $line =~ \{([a-zA-Z]+)\} ]]; do
      placeholder="${BASH_REMATCH[1]}"
      ok=0
      for k in "${known[@]}"; do
        [[ $k == "$placeholder" ]] && ok=1 && break
      done
      [[ $ok == 1 ]] || ptr_die 13 "config uses unknown placeholder '{$placeholder}' in an argv template"
      line="${line/\{"$placeholder"\}/}"
    done
  done <<<"$tokens"
}

# ---------------------------------------------------------------------------
# Path utilities
# ---------------------------------------------------------------------------

# Lexically normalize $1 to an absolute path (relative to $PWD if not already
# absolute). Purely string-based: never resolves symlinks and never requires
# the path to exist, so it works for a deleted file's path string (section
# 2.3, --files mode) and for the "nonexistent path" usage-error check.
ptr_abspath() {
  local input="$1" path
  if [[ $input == /* ]]; then
    path="$input"
  else
    path="$PWD/$input"
  fi

  local -a parts out
  parts=()
  out=()
  IFS='/' read -ra parts <<<"$path"
  local p n
  for p in "${parts[@]}"; do
    case "$p" in
    "" | ".") continue ;;
    "..")
      n=${#out[@]}
      if ((n > 0)); then
        out=("${out[@]:0:n-1}")
      fi
      ;;
    *) out+=("$p") ;;
    esac
  done

  if ((${#out[@]} == 0)); then
    printf '/'
  else
    local joined
    joined="$(
      IFS=/
      echo "${out[*]}"
    )"
    printf '/%s' "$joined"
  fi
}

# $1: root (absolute, normalized), $2: path (absolute, normalized, under root
# or equal to it). Prints the path relative to root ("" when path == root).
ptr_relpath() {
  local root="$1" dir="$2"
  if [[ $dir == "$root" ]]; then
    printf ''
  elif [[ $root == "/" ]]; then
    printf '%s' "${dir#/}"
  elif [[ $dir == "$root"/* ]]; then
    printf '%s' "${dir#"$root"/}"
  else
    printf '%s' "$dir"
  fi
}

# The repo TOPLEVEL: the nearest ancestor of $1 containing a `.git` entry
# (file or directory — a linked worktree's .git is a file), found by a plain
# upward walk with no git invocation (section 2.3). $1 need not exist.
# Prints the toplevel path and returns 0, or prints nothing and returns 1.
ptr_find_toplevel() {
  local dir="$1"
  while [[ ! -d $dir && $dir != "/" ]]; do
    dir="$(dirname "$dir")"
  done
  while true; do
    if [[ -e "$dir/.git" ]]; then
      printf '%s' "$dir"
      return 0
    fi
    [[ $dir == "/" ]] && return 1
    dir="$(dirname "$dir")"
  done
}

# ---------------------------------------------------------------------------
# Ignore matching (section 2.3: gitignore-style, both directions, subtree
# pruning — an ignored directory AND everything below it is pruned).
# ---------------------------------------------------------------------------

# Global: IGNORE_PATTERNS (array), populated by the caller from config.

# A single path segment (an ancestor prefix, relative to the ignore root)
# matches a pattern if: the pattern contains an internal slash (anchored —
# match the FULL relative path exactly), or it does not (match the BASENAME
# at any depth).
ptr_segment_matches_ignore() {
  local seg="$1" pat base
  for pat in "${IGNORE_PATTERNS[@]}"; do
    base="${pat%/}"
    if [[ $base == */* ]]; then
      [[ $seg == "$base" ]] && return 0
    else
      [[ ${seg##*/} == "$base" ]] && return 0
    fi
  done
  return 1
}

# $1: a path relative to the ignore root ("" for the root itself, never
# ignored). Ignored if $1 OR ANY ancestor prefix of $1 matches a pattern —
# this is what makes an ignored directory's descendants stay pruned even when
# their own basename doesn't match anything (e.g. a go.mod several levels
# under a `fixtures/` directory).
ptr_is_ignored() {
  local relpath="$1"
  [[ -z $relpath ]] && return 1

  local -a parts
  IFS='/' read -ra parts <<<"$relpath"
  local prefix="" part
  for part in "${parts[@]}"; do
    [[ -z $part ]] && continue
    if [[ -z $prefix ]]; then
      prefix="$part"
    else
      prefix="$prefix/$part"
    fi
    ptr_segment_matches_ignore "$prefix" && return 0
  done
  return 1
}

# ---------------------------------------------------------------------------
# Language registry (cached from config; section 2.1 `languages[]`)
# ---------------------------------------------------------------------------

# Globals populated: LANG_NAME[] (parallel to) LANG_JSON[] (compact JSON of
# that language's full config entry). Order preserved from the config file.
ptr_load_languages() {
  LANG_NAME=()
  LANG_JSON=()
  local count i
  count="$("$PTR_JQ" -r '.languages | length' "$PTR_CONFIG")"
  for ((i = 0; i < count; i++)); do
    LANG_NAME+=("$("$PTR_JQ" -r ".languages[$i].name" "$PTR_CONFIG")")
    LANG_JSON+=("$("$PTR_JQ" -c ".languages[$i]" "$PTR_CONFIG")")
  done
}

ptr_lang_json() {
  local name="$1" i
  for i in "${!LANG_NAME[@]}"; do
    if [[ ${LANG_NAME[$i]} == "$name" ]]; then
      printf '%s' "${LANG_JSON[$i]}"
      return 0
    fi
  done
  return 1
}

# Prints the name of every configured language whose markers glob-match
# inside directory $1, in configuration order. A directory matching several
# languages' markers yields several lines — deliberately: all of them run
# (section 2.3).
ptr_languages_matching_dir() {
  local dir="$1" i
  for i in "${!LANG_NAME[@]}"; do
    local -a markers
    mapfile -t markers < <("$PTR_JQ" -r '.markers[]?' <<<"${LANG_JSON[$i]}")
    local m matched=0
    for m in "${markers[@]}"; do
      if compgen -G "$dir/$m" >/dev/null 2>&1; then
        matched=1
        break
      fi
    done
    [[ $matched == 1 ]] && printf '%s\n' "${LANG_NAME[$i]}"
  done
}

# ---------------------------------------------------------------------------
# Discovery (section 2.3)
# ---------------------------------------------------------------------------

# Prints candidate directories nearest-first, walking up from $1 to $2
# (ceiling). $3 ("1"/"0"): whether the ceiling itself is a candidate.
ptr_upward_candidates() {
  local dir="$1" ceiling="$2" include_ceiling="$3"
  while true; do
    if [[ $dir == "$ceiling" ]]; then
      [[ $include_ceiling == 1 ]] && printf '%s\n' "$dir"
      return 0
    fi
    printf '%s\n' "$dir"
    [[ $dir == "/" ]] && return 0
    dir="$(dirname "$dir")"
  done
}

# Walks the candidates from ptr_upward_candidates, skipping ignored ones
# (continuing the walk PAST them — section 2.3), and stops at the nearest
# candidate that matches ANY language's markers, printing "path\tlang" for
# every matching language there. Prints nothing (and returns 1) if no
# candidate matches.
ptr_upward_resolve() {
  local start_dir="$1" ceiling="$2" include_ceiling="$3" relroot="$4"
  local cand rel
  while IFS= read -r cand; do
    [[ -z $cand ]] && continue
    rel="$(ptr_relpath "$relroot" "$cand")"
    ptr_is_ignored "$rel" && continue

    local -a langs
    mapfile -t langs < <(ptr_languages_matching_dir "$cand")
    if ((${#langs[@]} > 0)); then
      local l
      for l in "${langs[@]}"; do
        printf '%s\t%s\n' "$cand" "$l"
      done
      return 0
    fi
  done <<<"$(ptr_upward_candidates "$start_dir" "$ceiling" "$include_ceiling")"
  return 1
}

# Downward scan rooted at $1, relative to $2 for ignore matching. Prunes
# ignored subtrees in BOTH senses (never a project root, never descended
# into) and never follows symlinks. Does NOT stop at a discovered project
# root — real nesting requires descent (section 2.3).
ptr_downward_scan() {
  local dir="$1" root="$2"
  local rel
  rel="$(ptr_relpath "$root" "$dir")"
  ptr_is_ignored "$rel" && return 0

  local -a langs
  mapfile -t langs < <(ptr_languages_matching_dir "$dir")
  local l
  for l in "${langs[@]}"; do
    printf '%s\t%s\n' "$dir" "$l"
  done

  local entry
  while IFS= read -r -d '' entry; do
    ptr_downward_scan "$entry" "$root"
  done < <(find "$dir" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null)
}

# ---------------------------------------------------------------------------
# Mode resolvers. Each prints deduped-or-not "path\tlang" lines to stdout and
# MAY call ptr_die (exit 2 / exit 12) on a fatal resolution failure. Callers
# MUST invoke these via a plain `var="$(fn ...)"` (never a `<(...)` process
# substitution) so a die's exit code is observable as the assignment's exit
# status (section 2.6).
# ---------------------------------------------------------------------------

ptr_resolve_files_mode() {
  local toplevel
  toplevel="$(ptr_find_toplevel "$PWD")" || true
  if [[ -z $toplevel ]]; then
    ptr_die 2 "--files requires running inside a repository (no .git ancestor found from $PWD)"
  fi

  local f dir
  for f in "$@"; do
    dir="$(dirname "$(ptr_abspath "$f")")"
    ptr_upward_resolve "$dir" "$toplevel" 1 "$toplevel" || true
  done
}

ptr_resolve_paths_mode() {
  local input
  for input in "$@"; do
    local abs
    abs="$(ptr_abspath "$input")"
    if [[ ! -e $abs ]]; then
      ptr_die 2 "no such file or directory: $input"
    fi

    local start_dir
    if [[ -f $abs ]]; then
      start_dir="$(dirname "$abs")"
    else
      start_dir="$abs"
    fi

    local toplevel
    toplevel="$(ptr_find_toplevel "$start_dir")" || true

    local ceiling include_ceiling relroot
    if [[ -n $toplevel ]]; then
      relroot="$toplevel"
      if [[ $abs == "$toplevel" ]]; then
        ceiling="$toplevel"
        include_ceiling=1
      else
        # The toplevel is an up-walk candidate ONLY when the original path IS
        # the toplevel (section 2.3) — otherwise exclude it so a query below
        # a self-marked toplevel reaches the downward-scan fallback instead
        # of vacuously matching the root.
        ceiling="$toplevel"
        include_ceiling=0
      fi
    else
      relroot="/"
      ceiling="/"
      include_ceiling=1
    fi

    local matches
    matches="$(ptr_upward_resolve "$start_dir" "$ceiling" "$include_ceiling" "$relroot")" || true
    if [[ -n $matches ]]; then
      printf '%s\n' "$matches"
      continue
    fi

    local scan_relroot
    if [[ -n $toplevel ]]; then
      scan_relroot="$toplevel"
    else
      scan_relroot="$start_dir"
    fi
    matches="$(ptr_downward_scan "$start_dir" "$scan_relroot")" || true
    if [[ -n $matches ]]; then
      printf '%s\n' "$matches"
      continue
    fi

    ptr_die 12 "no project found for path (upward or downward): $input"
  done
}

ptr_resolve_all_mode() {
  local toplevel
  toplevel="$(ptr_find_toplevel "$PWD")" || true
  if [[ -z $toplevel ]]; then
    ptr_die 2 "--all requires running inside a repository (no .git ancestor found from $PWD)"
  fi
  ptr_downward_scan "$toplevel" "$toplevel"
}

# ---------------------------------------------------------------------------
# Placeholder substitution + per-project execution (sections 2.1, 2.4, 2.6)
# ---------------------------------------------------------------------------

ptr_cpu_count() {
  if command -v nproc >/dev/null 2>&1; then
    nproc
    return
  fi
  if command -v getconf >/dev/null 2>&1 && getconf _NPROCESSORS_ONLN >/dev/null 2>&1; then
    getconf _NPROCESSORS_ONLN
    return
  fi
  if command -v sysctl >/dev/null 2>&1 && sysctl -n hw.ncpu >/dev/null 2>&1; then
    sysctl -n hw.ncpu
    return
  fi
  printf '4'
}

# $1: the configured `jobs` value (string). "0" resolves to the CPU count.
ptr_jobs_value() {
  local configured="$1"
  if [[ $configured == "0" ]]; then
    ptr_cpu_count
  else
    printf '%s' "$configured"
  fi
}

# $1: this language's JSON, $2: a raw requested label. Applies labelAliases
# then labelPrefix (section 2.1).
ptr_map_label() {
  local lang_json="$1" label="$2" prefix alias
  prefix="$("$PTR_JQ" -r '.labelPrefix // ""' <<<"$lang_json")"
  alias="$("$PTR_JQ" -r --arg l "$label" '.labelAliases[$l] // $l' <<<"$lang_json")"
  printf '%s%s' "$prefix" "$alias"
}

# $1: this language's labelPrefix (may be empty). Uses the GLOBAL
# NON_UNIT_LABELS_JSON (the config's top-level nonUnitLabels, set once by the
# caller) — never a hardcoded string (section 2.1/2.4).
ptr_unit_exclusion() {
  local prefix="$1"
  "$PTR_JQ" -r --arg p "$prefix" 'map("!" + $p + .) | join(",")' <<<"$NON_UNIT_LABELS_JSON"
}

ptr_all_labels() {
  "$PTR_JQ" -r 'join(",")' <<<"$NON_UNIT_LABELS_JSON"
}

# Substring-level substitution of the five closed placeholders within one
# argv token (section 2.1).
ptr_render_token() {
  local token="$1" jobs_val="$2" labels_joined="$3" label_val="$4" unit_excl="$5" all_labels="$6"
  token="${token//\{jobs\}/$jobs_val}"
  token="${token//\{labels\}/$labels_joined}"
  token="${token//\{label\}/$label_val}"
  token="${token//\{unitExclusion\}/$unit_excl}"
  token="${token//\{allLabels\}/$all_labels}"
  printf '%s' "$token"
}

# Runs one argv template (JSON array, already placeholder-substituted values
# supplied) in $1 (project root), bounded by TIMEOUT_SECONDS. Returns the
# underlying tool's exit status; a `timeout` expiry (124) is reported as a
# failure naming the project and the cap (section 2.1/2.6).
ptr_invoke() {
  local path="$1" run_template="$2" jobs_val="$3" labels_joined="$4" label_val="$5" unit_excl="$6" all_labels="$7"
  local -a raw_tokens cmd
  mapfile -t raw_tokens < <("$PTR_JQ" -r '.[]' <<<"$run_template")
  cmd=()
  local tok
  for tok in "${raw_tokens[@]}"; do
    cmd+=("$(ptr_render_token "$tok" "$jobs_val" "$labels_joined" "$label_val" "$unit_excl" "$all_labels")")
  done

  local status
  (cd "$path" && "$PTR_TIMEOUT" "$TIMEOUT_SECONDS" "${cmd[@]}")
  status=$?
  if [[ $status -eq 124 ]]; then
    ptr_err "project $path timed out after ${TIMEOUT_SECONDS}s cap"
  fi
  return "$status"
}

# Executes one discovered (path, language) pair for the requested label
# selection. Prints one summary line (section 2.7) and returns 0 (ok/skip),
# 10 (failure), or 11 (tool missing) — never any other code, matching the
# execution-phase precedence in section 2.6.
ptr_execute_one() {
  local path="$1" lang="$2" labels_csv="$3"
  local lang_json
  lang_json="$(ptr_lang_json "$lang")" || ptr_die 13 "internal error: unknown language '$lang'"

  local -a tools
  mapfile -t tools < <("$PTR_JQ" -r '.tools[]?' <<<"$lang_json")
  local t
  for t in "${tools[@]}"; do
    if ! command -v "$t" >/dev/null 2>&1; then
      printf '[SKIP] %s (%s) required tool missing from PATH: %s — add it to the relevant home-manager profile\n' "$path" "$lang" "$t"
      return 11
    fi
  done

  local mode_key
  case "$labels_csv" in
  unit) mode_key="unit" ;;
  all) mode_key="all" ;;
  *) mode_key="labels" ;;
  esac

  local probe
  probe="$("$PTR_JQ" -c --arg k "$mode_key" '.run.probe[$k] // empty' <<<"$lang_json")"
  if [[ -n $probe && $probe != "null" ]]; then
    local -a probe_argv
    mapfile -t probe_argv < <("$PTR_JQ" -r '.[]' <<<"$probe")
    if ! (cd "$path" && "${probe_argv[@]}") >/dev/null 2>&1; then
      if [[ ${PTR_MODE:-} == "paths" ]]; then
        printf '[SKIP] %s (%s) probe vacuous for %s selection\n' "$path" "$lang" "$mode_key"
      fi
      return 0
    fi
  fi

  local run_template
  run_template="$("$PTR_JQ" -c --arg k "$mode_key" '.run[$k] // empty' <<<"$lang_json")"
  if [[ -z $run_template || $run_template == "null" ]]; then
    ptr_die 13 "language '$lang' has no run.$mode_key template configured"
  fi

  local label_prefix jobs_val unit_excl all_labels
  label_prefix="$("$PTR_JQ" -r '.labelPrefix // ""' <<<"$lang_json")"
  jobs_val="$(ptr_jobs_value "$JOBS_CONFIGURED")"
  unit_excl="$(ptr_unit_exclusion "$label_prefix")"
  all_labels="$(ptr_all_labels)"

  local has_label_placeholder=0
  if "$PTR_JQ" -e 'any(.[]; test("\\{label\\}"))' <<<"$run_template" >/dev/null 2>&1; then
    has_label_placeholder=1
  fi

  local -a labels_list
  labels_list=()
  if [[ $mode_key == "labels" ]]; then
    IFS=',' read -ra labels_list <<<"$labels_csv"
  fi

  local start_time end_time duration overall_ok=1
  start_time=$(date +%s)

  if [[ $mode_key == "labels" && $has_label_placeholder == 1 ]]; then
    local raw_label mapped_label
    for raw_label in "${labels_list[@]}"; do
      mapped_label="$(ptr_map_label "$lang_json" "$raw_label")"
      if ! ptr_invoke "$path" "$run_template" "$jobs_val" "" "$mapped_label" "$unit_excl" "$all_labels"; then
        overall_ok=0
      fi
    done
  else
    local labels_joined=""
    if [[ $mode_key == "labels" ]]; then
      local -a mapped
      mapped=()
      local rl
      for rl in "${labels_list[@]}"; do
        mapped+=("$(ptr_map_label "$lang_json" "$rl")")
      done
      labels_joined="$(
        IFS=,
        echo "${mapped[*]}"
      )"
    fi
    if ! ptr_invoke "$path" "$run_template" "$jobs_val" "$labels_joined" "" "$unit_excl" "$all_labels"; then
      overall_ok=0
    fi
  fi

  end_time=$(date +%s)
  duration=$((end_time - start_time))

  if [[ $overall_ok == 1 ]]; then
    printf '[PASS] %s (%s) %ss\n' "$path" "$lang" "$duration"
    return 0
  else
    printf '[FAIL] %s (%s) %ss\n' "$path" "$lang" "$duration"
    return 10
  fi
}

# $1: labels csv, remaining args: "path\tlang" project entries. Runs every
# project, prints one summary line each, and returns the aggregate exit code
# per section 2.6's execution-phase precedence: 10 > 11 > 0.
ptr_execute_projects() {
  local labels_csv="$1"
  shift
  local -a projects
  projects=("$@")

  ((${#projects[@]} == 0)) && return 0

  local any_failed=0 any_tool_missing=0
  local entry path lang rc
  for entry in "${projects[@]}"; do
    path="${entry%%$'\t'*}"
    lang="${entry#*$'\t'}"
    # Guarded (&&/||), NOT a plain statement: under the injected `set -e`, an
    # unguarded failing call here would abort the WHOLE run after the first
    # failing project instead of continuing across the remaining ones
    # (section 2.6: "the run continues across projects").
    rc=0
    ptr_execute_one "$path" "$lang" "$labels_csv" && rc=0 || rc=$?
    case "$rc" in
    10) any_failed=1 ;;
    11) any_tool_missing=1 ;;
    esac
  done

  if [[ $any_failed == 1 ]]; then
    return 10
  elif [[ $any_tool_missing == 1 ]]; then
    return 11
  fi
  return 0
}

# ---------------------------------------------------------------------------
# Top-level entry point, called by pg-test-runner.sh after argument parsing.
# ---------------------------------------------------------------------------

# $1: mode (files|paths|all), $2: labels csv, $3: config path, remaining: the
# mode's own arguments (files or paths; empty for `all`).
ptr_run() {
  local mode="$1" labels_csv="$2" config_path="$3"
  shift 3

  ptr_validate_config "$config_path"
  PTR_CONFIG="$config_path"
  PTR_MODE="$mode"
  ptr_load_languages
  mapfile -t IGNORE_PATTERNS < <("$PTR_JQ" -r '.ignore[]?' "$PTR_CONFIG")
  NON_UNIT_LABELS_JSON="$("$PTR_JQ" -c '.nonUnitLabels // []' "$PTR_CONFIG")"
  TIMEOUT_SECONDS="$("$PTR_JQ" -r '.timeoutSeconds' "$PTR_CONFIG")"
  JOBS_CONFIGURED="$("$PTR_JQ" -r '.jobs' "$PTR_CONFIG")"

  # Guarded so the exit-code check below always runs — matters when ptr_run is
  # exercised directly (e.g. a lib-level bats test) without the assembled
  # script's injected `set -e`, where an unguarded failing assignment would
  # otherwise just fall through with a stale $?.
  local raw resolve_rc=0
  case "$mode" in
  files) raw="$(ptr_resolve_files_mode "$@")" && resolve_rc=0 || resolve_rc=$? ;;
  paths) raw="$(ptr_resolve_paths_mode "$@")" && resolve_rc=0 || resolve_rc=$? ;;
  all) raw="$(ptr_resolve_all_mode)" && resolve_rc=0 || resolve_rc=$? ;;
  *) ptr_die 2 "internal error: unknown mode '$mode'" ;;
  esac
  if [[ $resolve_rc -ne 0 ]]; then
    exit "$resolve_rc"
  fi

  local -A seen
  seen=()
  local -a projects
  projects=()
  local line
  while IFS= read -r line; do
    [[ -z $line ]] && continue
    if [[ -z ${seen["$line"]:-} ]]; then
      seen["$line"]=1
      projects+=("$line")
    fi
  done <<<"$raw"

  local final_rc=0
  ptr_execute_projects "$labels_csv" "${projects[@]}" && final_rc=0 || final_rc=$?
  exit "$final_rc"
}
