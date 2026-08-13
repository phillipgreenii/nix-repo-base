# shellcheck shell=bash

# wsplan: the read-only Stage A land-plan emitter (design:
# docs/superpowers/specs/2026-08-12-land-plan-emitter-design.md).
#
# `land` is a two-stage operation. Stage A (this command) is READ-ONLY: it
# detects the workspace shape under an explicit `--root` and emits ONE typed
# JSON plan envelope. Stage B (the executor) mutates and runs in the main
# session, because subagent Bash calls do not persist cwd between calls. The
# plan therefore crosses an agent boundary AS RETURNED TEXT, which is what
# drives the input contract (§4), the wire format (§6) and the trust boundary
# (§6.3).
#
# `wsplan` is its OWN binary, NOT a `pnwf` subcommand (D2). Sharing
# modules/pnwf is a packaging choice only (D5): `pnwf` is by its own help the
# "deterministic helper for the workforest work-cycle", and housing the emitter
# there would put a workforest tool in charge of standalone repos that have no
# pn workspace at all — which IS the reach gap this command exists to close.
#
# This command MUST NOT write to any repo, create worktrees, prune, or fetch
# (§8). Note in particular that the stale-worktree filter in
# `wsplan_all_worktrees` DISCARDS prunable entries; it MUST NOT run
# `git worktree prune` to clean them.

show_help() {
  cat <<'HELP'
wsplan: read-only land-plan emitter for the WORKSPACE interface

Usage: wsplan land-plan --root <absolute-path> [--set-branch <name>]

Detects the shape of the work under <absolute-path> and prints ONE typed JSON
plan envelope on stdout for the Stage B executor to run. Read-only: it never
writes to a repo, creates or prunes a worktree, or fetches.

Subcommands:
  land-plan         Emit the plan envelope (the only subcommand).

Options:
  --root PATH       REQUIRED. Absolute path to an existing directory. Every
                     delegated command runs with its cwd pinned here; wsplan
                     derives no answer from its own inherited cwd.
  --set-branch NAME Optional. Selects the coordinated-workforest-set shape and
                     overrides the pointed-repo rule. The caller sources this
                     value from the tracker item; wsplan never reads a tracker.
  -h, --help        Show this help message
  -v, --version     Show version information

The envelope always carries version, outcome, shape, reason, steps and display:
  outcome    plan | nothing-to-do | refuse | stopped
  shape      single-repo | set | multi-repo | workspace, or null when the run
              stopped before classifying the input
  reason     null for plan/nothing-to-do; one of edges-present,
              ambiguous-target, detached-head, absent-ref, bad-path,
              missing-lock, not-a-repo, set-branch-required,
              incomplete-workspace, unsupported-layout, delegate-failed
  steps      ordered [{handler, targetWorktree}]; [] for every non-plan outcome
  display    free text for humans; display-only, sanitized, never parsed

Exit codes: 0 whenever an envelope was emitted, INCLUDING refuse and stopped —
those are honest answers, not failures. Non-zero (with a diagnostic on stderr
and NO envelope) only for a usage error: --root missing, relative or not an
existing directory, an unknown flag, or a --root that is inside a pn workspace
but is neither the workspace root nor inside any member repo.

Examples:
  wsplan land-plan --root /Users/me/workspace
  wsplan land-plan --root /Users/me/workspace/some-repo
  wsplan land-plan --root /Users/me/workspace --set-branch wf/my-feature
HELP
}

die() {
  echo "wsplan: error: $1" >&2
  exit "${2:-1}"
}

# --- state shared across the routers ----------------------------------------

# The normalized, physical `--root`. Every delegated command runs with cwd
# pinned here, and every containment test compares against it.
ROOT=""

# The INPUT shape as soon as routing picks a branch (workspace | single-repo |
# set). It is what a `stopped` envelope reports, and what row 1 of §7 means by
# "as detected"; `plan`/`refuse` shapes are refined from it by |TOUCHED|.
DETECTED_SHAPE=""

# Scan output accumulated across repos, one line per work area:
# "<repo><TAB><status><TAB><path>". A single string rather than an array so an
# empty scan needs no `set -u` gymnastics on the way into `wsplan_reduce`.
SCAN_TSV=""

# Set by `_scan_repo` when the scan must HALT. The reason code and its
# diagnostic are known to the scanner; the SHAPE is known only to the router,
# which is why the two are passed back rather than emitted in place.
STOP_REASON=""
STOP_DISPLAY=""

# repo name -> its canonical clone path, filled in as the workspace-root path
# enumerates members. Needed only so an ambiguity refusal can name each repo's
# canonical ONCE and its competing work areas relative to it — the rendering
# §6.3's 256-character cap forces (see `wsplan_relativize`).
declare -A REPO_CANONICAL=()

# --- emission ---------------------------------------------------------------

# Emits the one envelope and exits 0 (§6.2). Every terminal answer leaves
# through here, so "exactly one JSON object on stdout" is structural rather
# than a convention each branch has to remember.
_emit_and_exit() {
  wsplan_emit "$1" "$2" "$3" "$4" "${5:-[]}" ||
    die "internal: refused to emit a malformed envelope (see the diagnostic above)"
  exit 0
}

# `stopped` shorthand: _stopped <shape> <reason> <display>
_stopped() {
  _emit_and_exit stopped "$1" "$2" "$3"
}

# §7's row selection, keyed on |TOUCHED| (§5.4 step 2):
#   0  ⇒ nothing-to-do at the DETECTED input shape (row 1's "as detected")
#   1  ⇒ row 2's single-repo, whether reached by workspace-wide enumeration or
#        by the pointed-repo rule
#   2+ ⇒ rows 4-5's multi-repo
# A refusal reports the shape it WOULD have had (rows 4 and 6, "as detected").
_shape_for_touched() {
  case "$1" in
  0) printf '%s\n' "$DETECTED_SHAPE" ;;
  1) printf '%s\n' "single-repo" ;;
  *) printf '%s\n' "multi-repo" ;;
  esac
}

# --- delegation -------------------------------------------------------------

# Runs a delegated command with cwd PINNED to --root.
#
# REQUIRED, not stylistic: `pnwf` resolves the workspace from cwd ALONE and
# deliberately strips PN_WORKSPACE_ROOT (pnwf.sh:91-93), so a delegated call
# inheriting the fork's cwd would either die outside a workspace or silently
# resolve a DIFFERENT workspace. D4 forbids DEPENDING on an inherited cwd, not
# setting one deliberately — pinning cwd to the explicit --root is exactly what
# makes the emitter deterministic.
_at_root() {
  (cd "$ROOT" && "$@")
}

# `pn workspace info --json` with cwd pinned to --root and any inherited
# PN_WORKSPACE_ROOT cleared. The `env -u` is load-bearing for the same reason
# `_pnwf_info_json` has it: `pn` honors an exported PN_WORKSPACE_ROOT BEFORE
# its cwd walk, so a stale value would silently defeat the cwd pinning that IS
# this command's determinism guarantee.
_pn_info() {
  _at_root env -u PN_WORKSPACE_ROOT pn workspace info --json
}

# --- path handling ----------------------------------------------------------

# Prints <dir>'s PHYSICAL absolute path (symlinks resolved, no trailing slash).
#
# REQUIRED for every path this tool compares or emits: git reports PHYSICAL
# paths (`git worktree list` under macOS's /var reports /private/var), so
# comparing an unresolved --root against git's output would silently miss every
# containment test — and a silent containment miss is a wrong ROUTE, not a
# visible error.
_normalize_dir() {
  (cd "$1" >/dev/null 2>&1 && pwd -P)
}

# Boolean: is <child> equal to, or nested inside, <parent>?
_path_contains() {
  local parent="$1" child="$2"
  [ "$child" = "$parent" ] || [[ $child == "$parent"/* ]]
}

# Prints the space-separated <paths> with <base> stripped from each — the
# display-side rendering §6.3's 256-character cap forces (see
# `wsplan_relativize`). Word splitting the input is sound because every path
# here has already passed the §6.3 charset check, which excludes space.
_relativize_list() {
  local base="$1" paths="$2" out="" p
  local -a plist=()
  read -r -a plist <<<"$paths"
  for p in "${plist[@]}"; do
    out+="${out:+ }$(wsplan_relativize "$base" "$p")"
  done
  printf '%s' "$out"
}

# Walks UP from <dir> looking for pn-workspace.toml (§5.2 Q2), printing the
# directory that holds it; returns 1 when none exists at or above <dir>.
#
# Done first-party rather than by asking `pn`: `pn workspace info --json` FAILS
# outside a workspace, and conflating that failure with a real delegate failure
# would report stopped/delegate-failed for a perfectly ordinary standalone
# repo. Pure parameter expansion, no `dirname` subprocess per level.
_find_workspace_toml() {
  local dir="$1"
  while :; do
    if [ -f "$dir/pn-workspace.toml" ]; then
      printf '%s\n' "$dir"
      return 0
    fi
    if [ "$dir" = "/" ]; then
      return 1
    fi
    dir="${dir%/*}"
    if [ -z "$dir" ]; then
      dir="/"
    fi
  done
}

# --- scanning ---------------------------------------------------------------

# Scans ONE repo: enumerate its work areas (§5.1), classify each (§5.5/§8), and
# append "<repo><TAB><status><TAB><path>" to SCAN_TSV for every one.
#
# Returns 0 when the scan completed and 1 when it must HALT, in which case
# STOP_REASON/STOP_DISPLAY carry the §6.1 code and its diagnostic for the
# caller to emit at the shape only the caller knows.
#
# Deliberately NOT run inside a command substitution: it mutates SCAN_TSV, and
# an `exit` from a subshell would be swallowed while the emitter carried on.
_scan_repo() {
  local repo="$1" canonical="$2" rc=0 primary

  # `integrate-branch-support` IS one of §6.1's named delegates, so a failure
  # here is delegate-failed rather than a hard death.
  primary=$(pnwf_resolve_primary_branch "$canonical" 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    STOP_REASON="delegate-failed"
    STOP_DISPLAY="could not resolve the primary branch for '$repo' at $canonical: integrate-branch-support failed (rc=$rc)"
    return 1
  fi

  local areas_out
  rc=0
  areas_out=$(wsplan_work_areas "$canonical" "$primary") || rc=$?
  if [ "$rc" -ne 0 ]; then
    # git is not one of §6.1's delegates and there is deliberately no
    # catch-all reason, so an unexpected `git worktree list` failure in a
    # directory already confirmed to be a repo is reported as the emitter
    # DYING (non-zero, no envelope) rather than mislabelled as an honest
    # answer. That is exactly the distinction §6.2 exists to preserve.
    die "land-plan: could not enumerate work areas for '$repo' at $canonical: git worktree list failed (rc=$rc)"
  fi

  local -a areas=()
  if [ -n "$areas_out" ]; then
    mapfile -t areas <<<"$areas_out"
  fi
  if [ "${#areas[@]}" -eq 0 ]; then
    return 0
  fi

  local area cls status branch
  for area in "${areas[@]}"; do
    rc=0
    cls=$(wsplan_classify_work_area "$area" "$primary") || rc=$?
    if [ "$rc" -ne 0 ]; then
      die "land-plan: could not classify the work area $area of '$repo' (rc=$rc)"
    fi
    status="${cls%%$'\t'*}"
    branch="${cls#*$'\t'}"
    case "$status" in
    detached)
      STOP_REASON="detached-head"
      STOP_DISPLAY="'$repo' has a detached HEAD at $area; land cannot proceed until it is back on a branch"
      return 1
      ;;
    absent)
      STOP_REASON="absent-ref"
      STOP_DISPLAY="'$repo' at $area cannot be compared: branch '$branch' or primary '$primary' does not resolve"
      return 1
      ;;
    not-landed)
      if ! wsplan_path_ok "$area"; then
        # The offending path is withheld from `display` ON PURPOSE. `display`
        # lands verbatim in the main session's model context (§6.3) and this
        # path is precisely the attacker-influenceable text that failed the
        # charset check, so naming the repo is the useful half and quoting the
        # path is the dangerous half.
        STOP_REASON="bad-path"
        STOP_DISPLAY="an unlanded work area of '$repo' has a path outside the safe charset [A-Za-z0-9._/@+-] and cannot be handed to the executor; the path is withheld here deliberately"
        return 1
      fi
      SCAN_TSV+="$repo"$'\t'"not-landed"$'\t'"$area"$'\n'
      ;;
    landed | unborn)
      # `landed` is not a target. `unborn` — a repo with no commits — is
      # nothing-to-do rather than absent-ref (§5.5): an empty repo has nothing
      # to land, so it simply contributes no target.
      SCAN_TSV+="$repo"$'\t'"$status"$'\t'"$area"$'\n'
      ;;
    *)
      die "land-plan: internal: unknown work-area classification '$status' for $area"
      ;;
    esac
  done
  return 0
}

# --- routing: one repo (the pointed-repo path, D6, and the standalone path) --

# Emits the single-repo answer for <repo_name> whose canonical clone is
# <canonical_dir>, applying §5.4's reduction to that repo alone.
#
# The POINTED work area wins when --root names one directly (equal to, or
# nested inside, it). That is not a convenience: it IS D7's disambiguation
# mechanism — the caller resolves an ambiguous repo by pointing --root at the
# intended work area — so it MUST be tested before the ambiguity refusal.
# Matching takes the DEEPEST candidate, because linked worktrees commonly live
# INSIDE the canonical clone (this repo's own `.worktrees/<id>`), where a
# first-match walk would resolve a worktree to its enclosing canonical.
_route_single_repo() {
  local repo="$1" canonical="$2"
  DETECTED_SHAPE="single-repo"

  if ! _scan_repo "$repo" "$canonical"; then
    _stopped single-repo "$STOP_REASON" "$STOP_DISPLAY"
  fi

  local reduced
  reduced=$(printf '%s' "$SCAN_TSV" | wsplan_reduce)
  if [ -z "$reduced" ]; then
    _emit_and_exit nothing-to-do "$(_shape_for_touched 0)" "" \
      "nothing to land: '$repo' has no unlanded work area"
  fi

  # Exactly one repo row here by construction, so the repo name is already
  # known from the argument and its column is discarded.
  local count paths
  IFS=$'\t' read -r _ count paths <<<"$reduced"
  local -a area_list=()
  read -r -a area_list <<<"$paths"

  local target="" a
  for a in "${area_list[@]}"; do
    if _path_contains "$a" "$ROOT" && [ "${#a}" -gt "${#target}" ]; then
      target="$a"
    fi
  done

  if [ -z "$target" ]; then
    if [ "$count" -gt 1 ]; then
      # REMEDY FIRST, then the repo NAME, then each competing work area RELATIVE
      # to that repo's canonical — and no absolute path anywhere.
      #
      # §7 requires a refusal to explain its remedy and §5.4 step 3 requires it
      # to name the repo and its competing work areas, while §6.3 caps `display`
      # at 256 characters. Two REAL absolute work-area paths overflow that on
      # their own, and so does ONE absolute canonical plus prose once the tree is
      # deep (measured: a 155-character fixture root consumed the entire budget
      # and lost both area names). Anything that embeds an absolute path here is
      # therefore length-sensitive to its environment — silently fine in shallow
      # fixtures, silently lossy in a deep one. Naming only the repo and the
      # relative areas makes this message's length INDEPENDENT of tree depth,
      # and the caller can still reconstruct each path: it passed --root itself.
      _emit_and_exit refuse "$(_shape_for_touched 1)" ambiguous-target \
        "refuse: re-point --root at the intended work area. '$repo' has $count unlanded work areas: $(_relativize_list "$canonical" "$paths")"
    fi
    target="${area_list[0]}"
  fi

  local steps
  steps=$(wsplan_steps_json validate integrate-branch "$target") ||
    die "internal: could not build the steps array for $target"
  _emit_and_exit plan "$(_shape_for_touched 1)" "" \
    "1 repo to land: '$repo' at $target" "$steps"
}

# --- routing: the whole workspace -------------------------------------------

# Prints "<name><TAB><canonical_dir>" for the member repo that CONTAINS --root,
# or returns 1 when none does.
#
# Containment is tested against each member's canonical clone AND its linked
# worktrees (§5.2): real work lives in linked worktrees, and one may sit
# outside its canonical entirely (a coordinated set's member checkout does).
# The cheap direct test runs first and the worktree walk only if it finds
# nothing, so the common case costs no `git worktree list` at all.
_member_containing() {
  local info="$1" name path rc=0 best_name="" best_path="" best_len=0

  while IFS=$'\t' read -r name path; do
    if [ -z "$name" ] || [ ! -d "$path" ]; then
      continue
    fi
    path=$(_normalize_dir "$path") || continue
    if _path_contains "$path" "$ROOT" && [ "${#path}" -gt "$best_len" ]; then
      best_name="$name"
      best_path="$path"
      best_len="${#path}"
    fi
  done <<<"$(printf '%s' "$info" | jq -r '.repos[]? | [.name, .path] | @tsv')"

  if [ -n "$best_name" ]; then
    printf '%s\t%s\n' "$best_name" "$best_path"
    return 0
  fi

  while IFS=$'\t' read -r name path; do
    if [ -z "$name" ] || [ ! -d "$path" ]; then
      continue
    fi
    path=$(_normalize_dir "$path") || continue
    local wt_list wt
    rc=0
    wt_list=$(wsplan_all_worktrees "$path" 2>/dev/null) || rc=$?
    if [ "$rc" -ne 0 ] || [ -z "$wt_list" ]; then
      continue
    fi
    while IFS= read -r wt; do
      if _path_contains "$wt" "$ROOT"; then
        printf '%s\t%s\n' "$name" "$path"
        return 0
      fi
    done <<<"$wt_list"
  done <<<"$(printf '%s' "$info" | jq -r '.repos[]? | [.name, .path] | @tsv')"

  return 1
}

# Emits the workspace-wide answer: enumerate every member's work areas (§5.1),
# reduce to repos (§5.4), then select the §7 row.
_route_workspace() {
  local canonical_root="$1" info="$2"
  DETECTED_SHAPE="workspace"

  local lock="$canonical_root/pn-workspace.lock.json"

  # The CANONICAL workspace's lock, read EAGERLY — before any enumeration.
  # This path needs it twice over: §6.1 defines incomplete-workspace against
  # "a member named in the LOCK", and §5.6's edge test reads `.edges` from it.
  # Answering nothing-to-do for a workspace whose graph could not be read
  # would be exactly the silent miss §5.1 exists to prevent, so an unreadable
  # lock stops the run whatever |TOUCHED| would have been. (The SET path never
  # reaches here: `pnwf land-plan` reads the SET's own lock itself.)
  if ! wsplan_lock_readable "$lock"; then
    _stopped workspace missing-lock \
      "the workspace lock is missing or unreadable at $lock"
  fi

  local members_out rc=0
  members_out=$(pnwf_topo_order "$lock" 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    _stopped workspace missing-lock "could not read .order from $lock"
  fi
  local -a members=()
  if [ -n "$members_out" ]; then
    mapfile -t members <<<"$members_out"
  fi

  local member path
  for member in "${members[@]}"; do
    # The lock's `repos` entries carry only flake_path and remote_url — no
    # filesystem path — so the path source is `pn workspace info --json`'s
    # per-repo path (which is override-aware), falling back to the
    # canonical_root/<name> convention, never the lock alone.
    path=$(printf '%s' "$info" | jq -r --arg n "$member" '
      (.repos[]? | select(.name == $n) | .path) // empty')
    if [ -z "$path" ]; then
      path="$canonical_root/$member"
    fi
    if [ ! -d "$path" ]; then
      # At the workspace ROOT an absent member clone means an INCOMPLETELY
      # cloned workspace, not landed work — the opposite of the set path,
      # where an absent member directory is skipped as already-landed (§8).
      _stopped workspace incomplete-workspace \
        "member '$member' is named in $lock but has no clone on disk at $path; the workspace is incompletely cloned"
    fi
    path=$(_normalize_dir "$path") ||
      die "land-plan: could not resolve the path of member '$member' ($path)"
    REPO_CANONICAL["$member"]="$path"
    if ! _scan_repo "$member" "$path"; then
      _stopped workspace "$STOP_REASON" "$STOP_DISPLAY"
    fi
  done

  local reduced
  reduced=$(printf '%s' "$SCAN_TSV" | wsplan_reduce)

  local -a touched=() targets=()
  local ambiguous="" repo count paths
  while IFS=$'\t' read -r repo count paths; do
    if [ -z "$repo" ]; then
      continue
    fi
    touched+=("$repo")
    if [ "$count" -gt 1 ]; then
      # Same shape as the pointed-repo path's refusal — repo NAME plus its
      # competing work areas relative to that repo, never an absolute path — so
      # a consumer sees ONE format for §5.4 step 3 and the §6.3 cap can hold
      # several repos regardless of how deep the workspace sits.
      ambiguous+="${ambiguous:+; }'$repo' ($count): $(_relativize_list "${REPO_CANONICAL[$repo]}" "$paths")"
    else
      targets+=("$paths")
    fi
  done <<<"$reduced"

  local shape
  shape=$(_shape_for_touched "${#touched[@]}")

  # D7 is checked BEFORE the edge test and before row selection. A repo with
  # two unlanded work areas must NEVER reach the disjoint-multi-repo row,
  # which would ff-merge two DIFFERENT branches onto one primary and declare
  # them order-free.
  if [ -n "$ambiguous" ]; then
    # Remedy first, relativized evidence second — see the note on the
    # pointed-repo path above for why both halves matter under §6.3's cap.
    _emit_and_exit refuse "$shape" ambiguous-target \
      "refuse: re-point --root at the intended work area. More than one unlanded work area in: $ambiguous"
  fi

  if [ "${#touched[@]}" -eq 0 ]; then
    _emit_and_exit nothing-to-do "$shape" "" \
      "nothing to land: no member of the workspace at $canonical_root has an unlanded work area"
  fi

  if [ "${#touched[@]}" -ge 2 ]; then
    local edges
    rc=0
    edges=$(wsplan_direct_edges_among "$lock" "${touched[@]}" 2>/dev/null) || rc=$?
    if [ "$rc" -ne 0 ]; then
      _stopped "$shape" missing-lock "could not read .edges from $lock"
    fi
    if [ -n "$edges" ]; then
      # Remedy first, evidence second — same 256-character reasoning as the
      # ambiguity refusal below; a six-repo workspace can produce enough edge
      # lines to push a trailing remedy off the end.
      _emit_and_exit refuse "$shape" edges-present \
        "refuse: land these as ONE coordinated set (pn workspace workforest add), then re-run with --set-branch. Direct edge(s) among the repos to land: $(printf '%s' "$edges" | tr '\n' ';')"
    fi
  fi

  local steps touched_csv
  steps=$(wsplan_steps_json validate integrate-branch "${targets[@]}") ||
    die "internal: could not build the steps array for the workspace plan"
  touched_csv=$(
    IFS=,
    printf '%s' "${touched[*]}"
  )
  _emit_and_exit plan "$shape" "" \
    "${#touched[@]} repo(s) to land, order-free (no direct edge among them): $touched_csv" "$steps"
}

# --- routing: the coordinated workforest set --------------------------------

# Emits the set answer for <branch>. Member enumeration is DELEGATED to
# `pnwf land-plan` unchanged (D2 forbids modifying it, and the
# `land-workforest` skill already consumes its line-per-repo contract); this
# function derives the set DIRECTORY, which `pnwf land-plan` does not supply,
# and adds the HEAD sweep `pnwf land-plan` does not do.
_route_set() {
  local branch="$1"
  DETECTED_SHAPE="set"

  local info rc=0
  info=$(_pn_info 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ] || [ -z "$info" ]; then
    _stopped set delegate-failed \
      "'pn workspace info --json' failed (rc=$rc) with cwd pinned to $ROOT; --root may not be inside a pn workspace"
  fi

  local canonical_root workforests_dir
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root // empty')
  workforests_dir=$(printf '%s' "$info" | jq -r '.workforests_dir // empty')
  if [ -z "$canonical_root" ] || [ -z "$workforests_dir" ]; then
    _stopped set delegate-failed \
      "'pn workspace info --json' returned no canonical_root/workforests_dir for $ROOT"
  fi

  # An ABSOLUTE workforests_dir MUST refuse rather than compute a path (§5.3).
  # `pn` permits it (info.go:67,81), but `pnwf`'s own derivation is
  # unconditionally "$canonical_root/$workforests_dir/$branch"
  # (pnwf.sh:112-119), so the delegation below would build
  # "<canonical>//abs/sets/<branch>", miss the lock and die. Computing the
  # RIGHT path here while the delegate computes a broken one would emit an
  # envelope whose steps cannot execute; supporting the layout properly means
  # changing `pnwf`, which D2 forbids.
  case "$workforests_dir" in
  /*)
    _stopped set unsupported-layout \
      "workforests_dir is absolute ($workforests_dir); pnwf cannot consume that layout, so the set directory cannot be derived without changing pnwf"
    ;;
  esac

  local setdir="$canonical_root/$workforests_dir/$branch"

  # Combined stdout+stderr, matching cmd_update_relock's guarded relay: on
  # success `pnwf land-plan` prints only bare member names, and on failure the
  # captured diagnostic is what §6.1 requires `display` to carry for
  # delegate-failed. The shape check below is what makes the combined capture
  # safe — §6.1 scopes delegate-failed to "a non-zero exit OR UNUSABLE OUTPUT".
  local members_out
  rc=0
  members_out=$(_at_root pnwf land-plan "$branch" 2>&1) || rc=$?
  if [ "$rc" -ne 0 ]; then
    _stopped set delegate-failed \
      "'pnwf land-plan $branch' failed (rc=$rc) with cwd pinned to $ROOT: $members_out"
  fi

  local -a members=()
  if [ -n "$members_out" ]; then
    mapfile -t members <<<"$members_out"
  fi
  local m
  for m in "${members[@]}"; do
    if [[ ! $m =~ ^[A-Za-z0-9._-]+$ ]]; then
      _stopped set delegate-failed \
        "'pnwf land-plan $branch' produced unusable output where bare member names were expected: $members_out"
    fi
  done

  if [ "${#members[@]}" -eq 0 ]; then
    _emit_and_exit nothing-to-do set "" \
      "nothing to land: no member of the coordinated set '$branch' still needs landing"
  fi

  # Correction #9's set-path half. `pnwf land-plan` never inspects HEAD — it
  # tests worktree presence and ancestry in the canonical dir only
  # (pnwf.sh:466-477) — so the detached sweep is the emitter's OWN duty.
  #
  # Known residual (recorded in §8, not an oversight): this sweeps the members
  # `pnwf land-plan` enumerated, i.e. those not yet landed. A member whose
  # branch already landed but whose worktree sits detached is not swept;
  # closing that would need the emitter to enumerate set members itself.
  local wa
  for m in "${members[@]}"; do
    wa="$setdir/$m"
    # An absent member directory is skipped as already-landed, matching
    # `pnwf land-plan`. Scoped to the SET path (§8) — at the workspace root the
    # same condition is incomplete-workspace instead.
    if [ ! -d "$wa" ]; then
      continue
    fi
    if ! git -C "$wa" symbolic-ref -q HEAD >/dev/null 2>&1; then
      _stopped set detached-head \
        "member '$m' of the coordinated set '$branch' has a detached HEAD at $wa; land cannot proceed until it is back on a branch"
    fi
  done

  if ! wsplan_path_ok "$setdir"; then
    # Path withheld on purpose — see the bad-path branch of `_scan_repo`.
    _stopped set bad-path \
      "the directory of set '$branch' has a path outside the safe charset [A-Za-z0-9._/@+-] and cannot be handed to the executor; the path is withheld here deliberately"
  fi

  local steps members_csv
  steps=$(wsplan_steps_json validate-workforest land-workforest "$setdir") ||
    die "internal: could not build the steps array for set '$branch'"
  members_csv=$(
    IFS=,
    printf '%s' "${members[*]}"
  )
  _emit_and_exit plan set "" \
    "coordinated set '$branch' at $setdir, ${#members[@]} member(s) still to land: $members_csv" "$steps"
}

# --- the land-plan subcommand -----------------------------------------------

cmd_land_plan() {
  local root="" set_branch="" have_set_branch=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --root)
      [[ $# -ge 2 ]] || die "land-plan: --root requires a value"
      root="$2"
      shift
      ;;
    --root=*)
      root="${1#--root=}"
      ;;
    --set-branch)
      [[ $# -ge 2 ]] || die "land-plan: --set-branch requires a value"
      set_branch="$2"
      have_set_branch=1
      shift
      ;;
    --set-branch=*)
      set_branch="${1#--set-branch=}"
      have_set_branch=1
      ;;
    -h | --help)
      show_help
      exit 0
      ;;
    --*)
      die "land-plan: unknown argument: $1"
      ;;
    *)
      die "land-plan: unexpected positional argument: $1"
      ;;
    esac
    shift
  done

  # §4's preconditions, checked BEFORE any shape routing, so identical
  # malformed input yields the identical usage error with AND without
  # --set-branch. These are the four non-zero, NO-envelope cases of §6.2.
  [[ -n $root ]] || die "land-plan: --root is required"
  [[ $root == /* ]] || die "land-plan: --root must be an absolute path: $root"
  [[ -d $root ]] || die "land-plan: --root must name an existing directory: $root"
  if [[ $have_set_branch -eq 1 && -z $set_branch ]]; then
    die "land-plan: --set-branch requires a non-empty value"
  fi

  ROOT=$(_normalize_dir "$root") ||
    die "land-plan: could not resolve --root to a physical path: $root"

  # Q1: --set-branch selects the set shape and overrides the pointed-repo rule
  # (D6), so it is tested FIRST. That is also why a --root which is inside the
  # workspace but is neither the root nor a member is a usage error only on the
  # non-set path: `pn workspace info --json` is cwd-stable ANYWHERE in the
  # workspace, so the set directory resolves fine from such a --root.
  if [[ $have_set_branch -eq 1 ]]; then
    _route_set "$set_branch"
  fi

  # Q2: is there a pn workspace at or above --root?
  local ws_dir rc=0
  ws_dir=$(_find_workspace_toml "$ROOT") || rc=$?
  if [[ $rc -ne 0 ]]; then
    # Q2A: standalone. NOT exempt from §5.1 — normalize to the repo and
    # enumerate ITS work areas, for two reasons: a standalone repo whose
    # primary is landed but whose work sits in a linked worktree would
    # otherwise report nothing-to-do (the same silent miss §5.1 exists to
    # prevent), and a deep --root would otherwise emit a SUBDIRECTORY as
    # targetWorktree.
    local toplevel
    toplevel=$(git -C "$ROOT" rev-parse --show-toplevel 2>/dev/null) || toplevel=""
    if [[ -z $toplevel ]]; then
      _stopped "" not-a-repo \
        "$ROOT is outside any git repository and outside any pn workspace"
    fi
    local wt_list
    rc=0
    wt_list=$(wsplan_all_worktrees "$toplevel") || rc=$?
    if [[ $rc -ne 0 || -z $wt_list ]]; then
      die "land-plan: could not enumerate the worktrees of the repo at $toplevel"
    fi
    # The FIRST record is the main worktree, i.e. the canonical clone —
    # whichever worktree the list was run from (verified).
    local canonical="${wt_list%%$'\n'*}"
    _route_single_repo "${canonical##*/}" "$canonical"
  fi

  local info
  rc=0
  info=$(_pn_info 2>/dev/null) || rc=$?
  if [[ $rc -ne 0 || -z $info ]]; then
    _stopped "" delegate-failed \
      "'pn workspace info --json' failed (rc=$rc) with cwd pinned to $ROOT, although a pn-workspace.toml was found at $ws_dir"
  fi

  local in_workforest canonical_root
  in_workforest=$(printf '%s' "$info" | jq -r '.in_workforest')
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root // empty')

  # Q2B is REQUIRED, not defensive: a SET directory carries its OWN
  # pn-workspace.toml, so without this test a --root inside a set (absent
  # --set-branch) would be silently treated as a workspace ROOT. `pn` reports
  # in_workforest, so consult it rather than infer it from the path.
  if [[ $in_workforest == "true" ]]; then
    _stopped "" set-branch-required \
      "$ROOT is inside a coordinated workforest set; re-run with --set-branch <name> (the caller sources that value from the tracker item)"
  fi

  if [[ -z $canonical_root ]]; then
    _stopped "" delegate-failed \
      "'pn workspace info --json' returned no canonical_root for $ROOT"
  fi
  canonical_root=$(_normalize_dir "$canonical_root") ||
    die "land-plan: could not resolve canonical_root to a physical path: $canonical_root"

  # Q3: where is --root? The workspace root enumerates everything; a member
  # repo is single-repo for THAT repo with siblings ignored (D6, "pointed repo
  # wins"); anything else is a usage error.
  if [[ $ROOT == "$canonical_root" ]]; then
    _route_workspace "$canonical_root" "$info"
  fi

  local found=""
  found=$(_member_containing "$info") || found=""
  if [[ -n $found ]]; then
    local mname mpath
    IFS=$'\t' read -r mname mpath <<<"$found"
    _route_single_repo "$mname" "$mpath"
  fi

  die "land-plan: --root ($ROOT) is inside the pn workspace at $canonical_root but is neither the workspace root nor inside any member repo"
}

# --- top-level arg parsing + dispatch ---------------------------------------

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    break
    ;;
  --*)
    die "unknown option: $1"
    ;;
  *)
    break
    ;;
  esac
  shift
done

if [[ $# -eq 0 ]]; then
  show_help
  exit 1
fi

SUBCOMMAND="$1"
shift

case "$SUBCOMMAND" in
land-plan) cmd_land_plan "$@" ;;
*)
  die "unknown subcommand: $SUBCOMMAND"
  ;;
esac
