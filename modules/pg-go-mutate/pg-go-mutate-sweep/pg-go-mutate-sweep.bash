# shellcheck shell=bash
#
# pg-go-mutate-sweep shared library: plan enumeration, ledger, lock, and status
# classification. Sourced by pg-go-mutate-sweep.sh and directly by bats.
#
# Composed AFTER pg-go-mutate-lib, so pgm_detect_tags is in scope here.

pgms_die() {
  printf 'pg-go-mutate-sweep: %s\n' "$1" >&2
  return "${2:-1}"
}

# The state root deliberately reads XDG_STATE_HOME first: a session-scoped
# scratch directory is reclaimed out from under a long sweep (observed
# 2026-08-15, a full sweep's output destroyed two days after it ran).
pgms_state_root() {
  printf '%s/pg-go-mutate-sweep\n' "${XDG_STATE_HOME:-$HOME/.local/state}"
}

pgms_ledger_path() { printf '%s/ledger.jsonl\n' "$(pgms_state_root)"; }
pgms_runs_dir() { printf '%s/runs\n' "$(pgms_state_root)"; }

# Slugs are for FILESYSTEM paths only, never for keys: the mapping is not
# injective, which is why pgms_check_slug_collisions exists.
pgms_slug() { printf '%s\n' "${1//\//__}"; }

# '#' separates the two halves because it cannot appear in either: no path in
# this workspace contains one, and Go rejects it in an import-path element, so a
# '#' directory could never be a package `go list` enumerates.
pgms_unit_key() { printf '%s#%s\n' "$1" "$2"; }
pgms_unit_project() { printf '%s\n' "${1%%#*}"; }
pgms_unit_pkg() { printf '%s\n' "${1#*#}"; }

# The exclusion set is shared by project and package discovery. Each entry is
# load-bearing: without the fixtures prune the workspace yields 19 deliberately
# broken fixture modules instead of 16 real projects, .workforests holds full
# duplicate checkouts of every repo, and node_modules can contain a stray .go
# file that is harmless only because no go.mod sits above it.
_PGMS_PRUNE=(.git vendor node_modules .worktrees .workforests fixtures testdata)

_pgms_is_pruned() { # <relative-path>
  local p
  for p in "${_PGMS_PRUNE[@]}"; do
    case "/$1/" in */"$p"/*) return 0 ;; esac
  done
  return 1
}

# Project keys are workspace-root-relative paths, not basenames: a basename is
# not unique across six repos, and the key's first component is the repo label
# a project's bead carries. Normalized via dirname, matching _pgms_candidates:
# a go.mod AT the workspace root must key as "." rather than the literal string
# "go.mod" (stripping "/go.mod" off a bare "go.mod" is a no-op suffix removal).
pgms_find_projects() {
  local root="$1" f rel d
  [ -d "$root" ] || return 0
  while IFS= read -r f; do
    d="$(dirname "$f")"
    rel="${d#"$root"}"
    rel="${rel#/}"
    [ -z "$rel" ] && rel="."
    _pgms_is_pruned "$rel" && continue
    printf '%s\n' "$rel"
  done < <(find "$root" -type f -name go.mod 2>/dev/null) | sort
}

# Candidate dirs directly contain a non-test .go file. Nested candidates are
# dropped because pg-go-mutate walks its PATH argument RECURSIVELY, so an
# ancestor's run already covers its descendants.
_pgms_candidates() { # <project-abs-dir>
  local dir="$1" f rel d
  while IFS= read -r f; do
    case "$f" in *_test.go) continue ;; esac
    d="$(dirname "$f")"
    rel="${d#"$dir"}"
    rel="${rel#/}"
    [ -z "$rel" ] && rel="."
    _pgms_is_pruned "$rel" && continue
    printf '%s\n' "$rel"
  done < <(find "$dir" -type f -name '*.go' 2>/dev/null) | sort -u
}

# True if <candidate> lies strictly beneath <ancestor>. The repo-root ancestor
# "." is a special case: _pgms_candidates normalizes the root dir's own relative
# path to "." (never "./"), so an ordinary "$ancestor/*" glob can never match a
# root ancestor — every other candidate is unconditionally beneath it.
_pgms_is_nested() { # <candidate> <ancestor>
  local x="$1" y="$2"
  [ "$x" = "$y" ] && return 1
  [ "$y" = "." ] && return 0
  case "$x" in "$y"/*) return 0 ;; esac
  return 1
}

pgms_find_units() { # <root> <project-key>
  local root="$1" proj="$2" dirs x y keep
  dirs="$(_pgms_candidates "$root/$proj")"
  [ -n "$dirs" ] || return 0
  while IFS= read -r x; do
    [ -n "$x" ] || continue
    keep=1
    while IFS= read -r y; do
      [ -n "$y" ] || continue
      if _pgms_is_nested "$x" "$y"; then
        keep=0
        break
      fi
    done <<<"$dirs"
    [ "$keep" -eq 1 ] && printf '%s\n' "$x"
  done <<<"$dirs"
}

# A unit is a SUBTREE when another candidate lives beneath it. Subtree units sort
# last within their project so the cheap leaves bank findings first.
_pgms_is_subtree() { # <project-abs-dir> <pkg-rel>
  local dir="$1" pkg="$2" c
  while IFS= read -r c; do
    _pgms_is_nested "$c" "$pkg" && return 0
  done < <(_pgms_candidates "$dir")
  return 1
}

# Ordering: projects ascending by candidate count then key; within a project,
# leaves lexicographically then subtree units lexicographically. Deterministic,
# which is what makes resume stable across invocations.
pgms_plan() { # <root>
  local root="$1" proj units n leaves=() subs=() u
  while IFS= read -r proj; do
    [ -n "$proj" ] || continue
    units="$(pgms_find_units "$root" "$proj")"
    n="$(printf '%s\n' "$units" | grep -c . || true)"
    printf '%s\t%s\n' "$n" "$proj"
  done < <(pgms_find_projects "$root") | sort -n -k1,1 -k2,2 | while IFS=$'\t' read -r n proj; do
    leaves=()
    subs=()
    while IFS= read -r u; do
      [ -n "$u" ] || continue
      if _pgms_is_subtree "$root/$proj" "$u"; then subs+=("$u"); else leaves+=("$u"); fi
    done < <(pgms_find_units "$root" "$proj")
    for u in $(printf '%s\n' "${leaves[@]+"${leaves[@]}"}" | sort); do
      [ -n "$u" ] && pgms_unit_key "$proj" "$u"
    done
    for u in $(printf '%s\n' "${subs[@]+"${subs[@]}"}" | sort); do
      [ -n "$u" ] && pgms_unit_key "$proj" "$u"
    done
  done
}

# Slugs are not injective, so a collision would silently overwrite one unit's
# report with another's. Detected at PLAN time and fatal.
pgms_check_slug_collisions() { # <root>
  local root="$1" key proj pkg slug seen_file rc=0 prev
  seen_file="$(mktemp)"
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    proj="$(pgms_unit_project "$key")"
    pkg="$(pgms_unit_pkg "$key")"
    slug="$(pgms_slug "$proj")/$(pgms_slug "$pkg")"
    prev="$(grep -F "	$slug" "$seen_file" 2>/dev/null | head -1 | cut -f1)"
    if [ -n "$prev" ]; then
      printf 'pg-go-mutate-sweep: slug collision: %s and %s both map to %s\n' "$prev" "$key" "$slug" >&2
      rc=2
    fi
    printf '%s\t%s\n' "$key" "$slug" >>"$seen_file"
  done < <(pgms_plan "$root")
  rm -f "$seen_file"
  return "$rc"
}

# Statuses whose re-attempt is likely to differ. This is a REAL partition, and
# `--retry transient` is its shorthand; by default NO status is re-attempted, so
# a re-run always makes forward progress and never loops on a broken unit.
_PGMS_TRANSIENT="not-enumerable unhealthy vanished inconclusive timeout failed"

pgms_append_record() { # <json-line>
  mkdir -p "$(pgms_state_root)"
  printf '%s\n' "$1" >>"$(pgms_ledger_path)"
}

# Per-line validation, not `jq -s`: a kill -9 can truncate the final line, and
# slurping would then fail over the WHOLE ledger rather than skipping one line.
pgms_valid_lines() {
  local line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '%s' "$line" | jq -e 'type == "object"' >/dev/null 2>&1 && printf '%s\n' "$line"
  done
}

pgms_replay_units() {
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs 'map(select(.kind == "unit")) | group_by(.unit) | map(last)
            | .[] | "\(.unit)\t\(.status)"'
}

pgms_unit_status() { # <unit-key>
  pgms_replay_units | awk -F'\t' -v u="$1" '$1 == u { print $2 }' | tail -1
}

pgms_unit_needs_run() { # <unit-key> <retry-spec>
  local unit="$1" spec="${2:-}" status s
  status="$(pgms_unit_status "$unit")"
  [ -z "$status" ] && return 0
  [ -z "$spec" ] && return 1
  if [ "$spec" = "transient" ]; then
    for s in $_PGMS_TRANSIENT; do [ "$s" = "$status" ] && return 0; done
    return 1
  fi
  printf ',%s,' "$spec" | grep -qF ",$status," && return 0
  return 1
}

_pgms_latest_bead() { # <project> -> the whole record, or empty
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs --arg p "$1" 'map(select(.kind == "bead" and .project == $p))
                         | if length == 0 then empty else (last | @json) end'
}

# Unit records in this task's own ledger carry no "project" field (only the
# "#"-joined key), while a later task's records include it. The fallback
# derives the project from the key when the field is absent, so this predicate
# sees a "newer unit" either way -- without it the amend path (predicate 2)
# never fires against this task's own fixtures and the retried-findings hole
# reopens. Controller ruling: use this form from the start, not the brief's
# bare ".project == $p".
_pgms_newest_unit_stamp() { # <project> -> RFC3339 or empty
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs --arg p "$1" '
      map(select(.kind == "unit"))
      | map(select((.project // (.unit | split("#")[0])) == $p))
      | map(.finished // "")
      | max // empty'
}

# Predicate 2, evaluated by REPLAY at startup and after every unit record --
# never as an in-loop "last unit" event. The event form files nothing for a
# project whose units are all already recorded, which is the loss this closes.
pgms_bead_due() { # <root> <project>
  local root="$1" proj="$2" units u bead bead_stamp newest
  units="$(pgms_find_units "$root" "$proj")"
  [ -n "$units" ] || return 1 # a zero-unit project is never due
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    [ -z "$(pgms_unit_status "$(pgms_unit_key "$proj" "$u")")" ] && return 1
  done <<<"$units"

  bead="$(_pgms_latest_bead "$proj")"
  [ -z "$bead" ] && return 0 # all recorded, never filed

  bead_stamp="$(printf '%s' "$bead" | jq -r '.finished // ""')"
  newest="$(_pgms_newest_unit_stamp "$proj")"
  # Strictly newer, so appending the bead record makes this false again and the
  # amend cannot loop. A same-second tie resolves in the terminating direction.
  [ -n "$newest" ] && [ -n "$bead_stamp" ] && [[ $newest > $bead_stamp ]] && return 0
  return 1
}

# A suppressed marker carries no id, so there is nothing to comment on: file.
pgms_bead_action() { # <root> <project> -> file | amend
  local bead id
  bead="$(_pgms_latest_bead "$2")"
  id="$(printf '%s' "$bead" | jq -r '.bead // ""' 2>/dev/null || true)"
  if [ -n "$bead" ] && [ -n "$id" ]; then printf 'amend\n'; else printf 'file\n'; fi
}
