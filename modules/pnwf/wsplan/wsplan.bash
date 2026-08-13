# shellcheck shell=bash

# wsplan.bash: the detection, reduction, edge-test and envelope-construction
# primitives behind the `wsplan` land-plan emitter (design:
# docs/superpowers/specs/2026-08-12-land-plan-emitter-design.md).
#
# The .sh/.bash split is REQUIRED here, not stylistic (design §3): shape
# detection, the §5.4 reduction and the §5.6 edge test are pure functions over
# their inputs and MUST be unit-testable without going through argument
# parsing. `wsplan.sh` owns argument parsing, routing and the exit-code
# contract; everything here is a probe or a builder.
#
# Every function runs under the builder's injected `set -euo pipefail`, so a
# git/jq call whose exit code IS the meaningful signal is guarded in the shape
# pnwf-lib.bash documents:
#
#   rc=0
#   git ... || rc=$?
#
# Reuse obligation (design §3.2): primary-branch resolution and the ancestor
# check come from pnwf-lib (`pnwf_resolve_primary_branch`,
# `pnwf_is_ancestor_of_primary`, `pnwf_topo_order`) and are NOT reimplemented
# here.

# Envelope schema version (design §6). Bumped only by a wire-format change.
WSPLAN_ENVELOPE_VERSION=1

# The four closed enums of design §6/§6.1. Declared as arrays (not
# space-separated strings) so membership tests need no word splitting.
WSPLAN_OUTCOMES=(plan nothing-to-do refuse stopped)
WSPLAN_SHAPES=(single-repo set multi-repo workspace)
WSPLAN_HANDLERS=(validate integrate-branch validate-workforest land-workforest)
WSPLAN_REASONS=(
  edges-present
  ambiguous-target
  detached-head
  absent-ref
  bad-path
  missing-lock
  not-a-repo
  set-branch-required
  incomplete-workspace
  unsupported-layout
  delegate-failed
)

# Boolean: is $1 one of the remaining arguments?
_wsplan_in_list() {
  local needle="$1" item
  shift
  for item in "$@"; do
    if [ "$item" = "$needle" ]; then
      return 0
    fi
  done
  return 1
}

# --- trust boundary (design §6.3) -------------------------------------------

# Prints <text> sanitized for the envelope's `display` field: every control
# character removed, newlines/CRs/tabs collapsed to single spaces, runs of
# spaces squeezed, ends trimmed, and the result capped at 256 characters.
#
# This is a REQUIREMENT, not tidiness. `display` is consumed by a MODEL, not a
# shell: the plan crosses an agent boundary as returned text into the main
# session, where the Stage B executor holds mutating authority. `jq --arg`
# guarantees valid JSON, not benign text, and "Stage B MUST NOT parse it" does
# not bind an LLM — so instruction-shaped or layout-shaped text in a branch or
# repo name must not survive verbatim.
wsplan_sanitize_display() {
  local text="$1"
  # Two passes: fold the layout characters to spaces FIRST (so a newline
  # becomes a separator rather than vanishing and joining two words), then
  # delete every remaining control character.
  text=$(printf '%s' "$text" | tr '\n\r\t\v\f' '     ' | tr -d '\000-\037\177')
  text=$(printf '%s' "$text" | tr -s ' ')
  text="${text# }"
  text="${text% }"
  printf '%s' "${text:0:256}"
}

# Prints <path> with <base>'s prefix stripped — a repo-relative path — or <path>
# unchanged when it is not under <base>. <base> itself prints as ".".
#
# For `display` ONLY; `targetWorktree` is NEVER relativized. §6.3 caps `display`
# at 256 characters while §5.4 step 3 wants the repo AND its competing work areas
# named and §7 wants the remedy explained — and two REAL absolute work-area paths
# alone overflow that cap (observed live, and reproduced by the nix sandbox's
# longer TMPDIR). Naming the repo's canonical path ONCE and each area relative to
# it keeps all three inside the cap while still letting a reader reconstruct every
# absolute path.
wsplan_relativize() {
  local base="$1" path="$2"
  if [ "$path" = "$base" ]; then
    printf '%s' "."
  elif [[ $path == "$base"/* ]]; then
    printf '%s' "${path#"$base"/}"
  else
    printf '%s' "$path"
  fi
}

# Boolean: does <path> match the design §6.3 charset ^[A-Za-z0-9._/@+-]+$ ?
#
# Deliberately narrow. `targetWorktree` is attacker-influenceable AND reaches a
# command line — Stage B `cd`s to it, and git permits shell metacharacters in
# branch names (`git check-ref-format 'refs/heads/x$(id)'` exits 0). The
# charset excludes space, `~`, `:`, `,`, `=`, `#`, `(`, `)`; it is verified
# sufficient for every real path this emitter produces (workspace root, member
# clone, linked worktree, set dir, macOS `mktemp -d` fixture). A legitimate
# path containing a space is a DELIBERATE rejection, not an oversight: do not
# widen this without replacing it with a stronger guarantee at the Stage B
# boundary.
wsplan_path_ok() {
  [[ $1 =~ ^[A-Za-z0-9._/@+-]+$ ]]
}

# --- envelope construction (design §6) --------------------------------------

# Prints the `steps` array: for each work-area path, in order, a <h1> step then
# a <h2> step, both at that path. With no paths it prints `[]`.
#
# Handlers take NO arguments — they re-derive everything from git — so the only
# variable content is the path, and both the handler name and the path are
# validated here rather than trusted of the caller: an unknown handler or a
# charset-violating path must never reach the envelope.
wsplan_steps_json() {
  local h1="$1" h2="$2"
  shift 2
  local h p
  for h in "$h1" "$h2"; do
    if ! _wsplan_in_list "$h" "${WSPLAN_HANDLERS[@]}"; then
      echo "wsplan_steps_json: unknown handler: $h" >&2
      return 1
    fi
  done
  if [ "$#" -eq 0 ]; then
    printf '[]\n'
    return 0
  fi
  for p in "$@"; do
    if ! wsplan_path_ok "$p"; then
      echo "wsplan_steps_json: path fails the design §6.3 charset check" >&2
      return 1
    fi
  done
  # `--args` puts every remaining argument in $ARGS.positional, so no path is
  # ever interpolated into the jq program text.
  jq -n --arg h1 "$h1" --arg h2 "$h2" --args '
    [ $ARGS.positional[]
      | { handler: $h1, targetWorktree: . },
        { handler: $h2, targetWorktree: . }
    ]
  ' "$@"
}

# Emits THE ONE JSON envelope (design §6) on stdout.
#
#   wsplan_emit <outcome> <shape> <reason> <display> [steps_json]
#
# An EMPTY <shape> or <reason> means JSON `null`. <steps_json> defaults to `[]`.
#
# Every §6 field invariant is asserted here rather than trusted of the caller.
# That is deliberate: a malformed envelope would hand Stage B — which holds
# mutating authority in the main session — an unparseable plan, and the
# discriminator (`outcome`) is the ONLY field Stage B branches on. A violation
# is an internal defect, so it returns non-zero with a diagnostic instead of
# emitting anything.
#
# Built with `jq -n --arg/--argjson` only; string concatenation into JSON is
# forbidden (§6.3).
wsplan_emit() {
  local outcome="$1" shape="$2" reason="$3" display="$4" steps="${5:-[]}"

  if ! _wsplan_in_list "$outcome" "${WSPLAN_OUTCOMES[@]}"; then
    echo "wsplan_emit: unknown outcome: $outcome" >&2
    return 1
  fi
  if [ -n "$shape" ] && ! _wsplan_in_list "$shape" "${WSPLAN_SHAPES[@]}"; then
    echo "wsplan_emit: unknown shape: $shape" >&2
    return 1
  fi
  if [ -n "$reason" ] && ! _wsplan_in_list "$reason" "${WSPLAN_REASONS[@]}"; then
    echo "wsplan_emit: unknown reason: $reason" >&2
    return 1
  fi

  # `reason` MUST be null for plan/nothing-to-do and non-null for refuse/stopped.
  case "$outcome" in
  plan | nothing-to-do)
    if [ -n "$reason" ]; then
      echo "wsplan_emit: outcome '$outcome' MUST carry a null reason (got '$reason')" >&2
      return 1
    fi
    ;;
  refuse | stopped)
    if [ -z "$reason" ]; then
      echo "wsplan_emit: outcome '$outcome' MUST carry a non-null reason" >&2
      return 1
    fi
    ;;
  esac

  # `steps` MUST be [] for every outcome other than `plan`, and non-empty for
  # `plan` (a plan with no steps is not a plan).
  if [ "$outcome" = "plan" ]; then
    if [ "$steps" = "[]" ]; then
      echo "wsplan_emit: outcome 'plan' MUST carry at least one step" >&2
      return 1
    fi
  elif [ "$steps" != "[]" ]; then
    echo "wsplan_emit: outcome '$outcome' MUST carry an empty steps array" >&2
    return 1
  fi

  jq -n \
    --argjson version "$WSPLAN_ENVELOPE_VERSION" \
    --arg outcome "$outcome" \
    --arg shape "$shape" \
    --arg reason "$reason" \
    --argjson steps "$steps" \
    --arg display "$(wsplan_sanitize_display "$display")" \
    '{
      version: $version,
      outcome: $outcome,
      shape: (if $shape == "" then null else $shape end),
      reason: (if $reason == "" then null else $reason end),
      steps: $steps,
      display: $display
    }'
}

# --- where work actually lives (design §5.1) --------------------------------

# Prints every worktree path git knows for <repo_dir>, one per line, the MAIN
# worktree FIRST, with design §5.1's two mandatory filters applied.
#
# Used for path CONTAINMENT (which member repo does --root live in, D6), where
# the canonical clone is a legitimate answer — unlike `wsplan_work_areas`,
# which excludes a canonical sitting on its primary branch.
wsplan_all_worktrees() {
  local repo_dir="$1" rc=0 porcelain
  porcelain=$(git -C "$repo_dir" worktree list --porcelain 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "wsplan_all_worktrees: git worktree list failed in $repo_dir (rc=$rc)" >&2
    return "$rc"
  fi

  local line path="" prunable=0
  # Each porcelain record is terminated by a BLANK line, and `$(...)` strips
  # the trailing one — so a newline is appended back here, or the LAST record
  # would never be flushed.
  while IFS= read -r line; do
    case "$line" in
    "worktree "*)
      path="${line#worktree }"
      prunable=0
      ;;
    prunable*)
      prunable=1
      ;;
    "")
      # Design §5.1's mandatory filters: an entry marked `prunable`, or whose
      # path is not an existing directory, MUST be discarded. Admin entries
      # under .git/worktrees linger until an explicit `git worktree prune`
      # (pnwf-lib.bash:50-55), so a list-based walk otherwise reports a stale
      # "present" for a directory already removed from disk — which would then
      # fail the §8 symbolic-ref probe and halt the land as
      # stopped/detached-head for a path that does not exist. wsplan is
      # READ-ONLY: the stale entry is DISCARDED, never pruned (§8).
      if [ -n "$path" ] && [ "$prunable" -eq 0 ] && [ -d "$path" ]; then
        printf '%s\n' "$path"
      fi
      path=""
      prunable=0
      ;;
    esac
  done <<<"$porcelain"$'\n'
}

# Prints one repo's candidate WORK AREAS (design §5.1), one absolute path per
# line: every LINKED worktree first (in `git worktree list` order), then the
# canonical clone iff it is off its primary branch.
#
#   1. Every LINKED worktree. The FIRST porcelain record is the main worktree
#      (verified: it reports the canonical clone on its primary branch,
#      whichever worktree the list is run from) and is skipped here — item 2
#      owns the canonical.
#   2. The canonical clone itself, but ONLY when its HEAD is not <primary> — an
#      R-3 anomaly that MUST be surfaced rather than hidden. A DETACHED
#      canonical qualifies (symbolic-ref fails) and is then reported by §8.
#
# Walking LINKED worktrees rather than canonical clones is load-bearing: Tier R
# (R-3) keeps every canonical clone clean and on its primary branch in steady
# state, so a canonical-only walk can never find anything — it would report
# nothing-to-do while branches await landing, and the multi-repo rows of §7
# would be unreachable dead code.
wsplan_work_areas() {
  local canonical_dir="$1" primary="$2" rc=0 list
  list=$(wsplan_all_worktrees "$canonical_dir") || rc=$?
  if [ "$rc" -ne 0 ]; then
    return "$rc"
  fi

  local -a all=()
  if [ -n "$list" ]; then
    mapfile -t all <<<"$list"
  fi
  if [ "${#all[@]}" -eq 0 ]; then
    echo "wsplan_work_areas: no usable worktree records in $canonical_dir" >&2
    return 1
  fi

  local i
  for ((i = 1; i < ${#all[@]}; i++)); do
    printf '%s\n' "${all[$i]}"
  done

  local head_rc=0 head_branch
  head_branch=$(git -C "${all[0]}" symbolic-ref --short -q HEAD) || head_rc=$?
  if [ "$head_rc" -ne 0 ] || [ "$head_branch" != "$primary" ]; then
    printf '%s\n' "${all[0]}"
  fi
}

# --- what "landed" means (design §5.5, §8) ----------------------------------

# Classifies ONE work area against <primary>. Prints "<status><TAB><branch>":
#
#   detached     HEAD is not a symref. Correction #9: this HALTS the land
#                (stopped/detached-head); it is never a non-target and never
#                silently skipped. <branch> is empty.
#   unborn       HEAD is a symref to an uncreated ref — a repo with no commits.
#                symbolic-ref SUCCEEDS here, so the detached test correctly
#                passes, but the ancestry check would then answer `absent`;
#                §5.5 classifies this nothing-to-do, NOT absent-ref, because an
#                empty repo has nothing to land.
#   landed       an ancestor of <primary>: not a target, skip.
#   not-landed   an unlanded work area.
#   absent       the ancestry comparison could not resolve a ref (rc 128) ⇒
#                §5.5's stopped/absent-ref. MUST NOT be silently skipped.
#
# The symbolic-ref probe runs BEFORE any ancestry comparison, so a detached
# HEAD is never compared (§5.5). Never aborts under `set -e` for any of the
# five answers; a genuinely unexpected git failure propagates its rc.
wsplan_classify_work_area() {
  local work_area="$1" primary="$2" rc=0 branch status
  branch=$(git -C "$work_area" symbolic-ref --short -q HEAD) || rc=$?
  if [ "$rc" -ne 0 ]; then
    printf 'detached\t\n'
    return 0
  fi
  if ! git -C "$work_area" rev-parse -q --verify HEAD >/dev/null 2>&1; then
    printf 'unborn\t%s\n' "$branch"
    return 0
  fi
  rc=0
  status=$(pnwf_is_ancestor_of_primary "$work_area" "$branch" "$primary") || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "wsplan_classify_work_area: ancestry check failed in $work_area (rc=$rc)" >&2
    return "$rc"
  fi
  printf '%s\t%s\n' "$status" "$branch"
}

# --- the reduction rule (design §5.4) ---------------------------------------

# The §5.4 reduction: work areas (per-repo and PLURAL) down to repos (what
# routing, the edge test and `targetWorktree` are keyed on).
#
# Reads one scan line per work area on stdin:
#
#     <repo><TAB><status><TAB><path>
#
# and prints ONE line per repo holding at least one `not-landed` area, in
# first-seen order:
#
#     <repo><TAB><count><TAB><path>[ <path>…]
#
# `TOUCHED` is EXACTLY the set of repo names printed here. Both §5.6's edge
# test and §7's row selection operate on THAT, never on work areas — and a
# <count> above 1 is D7's ambiguity, which the caller MUST turn into
# refuse/ambiguous-target rather than picking one or emitting a step per work
# area. Leaving this reduction implicit is what let an earlier draft route a
# repo with two unlanded worktrees to the "disjoint multi-repo" row, emitting
# two ff-merges of two DIFFERENT branches onto one primary and calling them
# order-free.
#
# Space-separating the paths is sound ONLY because every `not-landed` path has
# already passed `wsplan_path_ok`, whose charset (§6.3) excludes space; the
# caller runs that check at classification time and stops with bad-path
# otherwise.
wsplan_reduce() {
  local line repo status path
  local -a order=()
  local -A areas=()
  while IFS=$'\t' read -r repo status path; do
    if [ -z "$repo" ] || [ "$status" != "not-landed" ]; then
      continue
    fi
    if [ -z "${areas[$repo]+set}" ]; then
      order+=("$repo")
      areas["$repo"]="$path"
    else
      areas["$repo"]+=" $path"
    fi
  done

  if [ "${#order[@]}" -eq 0 ]; then
    return 0
  fi
  local r
  local -a paths=()
  for r in "${order[@]}"; do
    read -r -a paths <<<"${areas[$r]}"
    printf '%s\t%s\t%s\n' "$r" "${#paths[@]}" "${areas[$r]}"
  done
}

# --- the edge test (design §5.6) --------------------------------------------

# Boolean: is <lock_file> present, readable and parseable JSON? Backs §6.1's
# `missing-lock`, whose condition is "missing OR unreadable".
wsplan_lock_readable() {
  local lock_file="$1"
  [ -f "$lock_file" ] && [ -r "$lock_file" ] && jq -e . "$lock_file" >/dev/null 2>&1
}

# The §5.6 edge test over TOUCHED (repo NAMES, per §5.4). Prints one
# "<consumer> -> <target>" line per DIRECT edge whose consumer AND target are
# BOTH touched; EMPTY output means the touched set is disjoint.
#
#     disjoint(TOUCHED) ⇔ ¬∃ e ∈ .edges : e.consumer ∈ TOUCHED ∧ e.target ∈ TOUCHED
#
# DIRECT edges only. That is not an approximation but exactly the rule that
# transitive edges through UNTOUCHED repos do not count: with A → B → C and
# only A and C touched, A and C are disjoint. MUST NOT compute a transitive
# closure.
wsplan_direct_edges_among() {
  local lock_file="$1"
  shift
  local rc=0 out
  # `. as $e` binds the edge object BEFORE the `$touched | index(...)` pipe,
  # which rebinds `.` to the $touched ARRAY while evaluating its argument — a
  # bare `.consumer` there indexes the array and is a jq type error. Same trap
  # pnwf.sh:369-372 documents for its --repos filter.
  out=$(jq -n -r --slurpfile lock "$lock_file" --args '
    ($ARGS.positional) as $touched
    | ($lock[0].edges // [])[]
    | . as $e
    | select(($touched | index($e.consumer)) != null
             and ($touched | index($e.target)) != null)
    | "\($e.consumer) -> \($e.target)"
  ' "$@" 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "wsplan_direct_edges_among: could not read .edges from $lock_file (rc=$rc)" >&2
    return "$rc"
  fi
  if [ -n "$out" ]; then
    printf '%s\n' "$out"
  fi
}
