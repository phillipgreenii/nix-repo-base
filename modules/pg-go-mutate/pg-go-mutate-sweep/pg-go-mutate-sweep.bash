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
