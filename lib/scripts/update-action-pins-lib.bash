# shellcheck shell=bash
# update-action-pins-lib.bash — the shared update-locks step that refreshes GitHub Action
# SHA pins (bead pg2-ehu9q; operator decision 2026-10-06, IT-audit handoff pg2-c83co C.4:
# pin every GitHub Action to a full commit SHA, BUT every repo's update-locks.sh MUST be able
# to move the pins). Sourced by update-locks-lib.bash, so a consumer that already sources
# that lib gets ul_refresh_action_pins for free.
#
# Consumer usage (in update-locks.sh, after ul_setup):
#
#   ul_run_step "github-action-pins" \
#     "update-locks: refresh GitHub Action SHA pins" \
#     ul_refresh_action_pins
#
# =================================================================
# CONTRACT
# =================================================================
#
# ul_refresh_action_pins [--pin-mutable] [--workflows-dir DIR]
#
#   Scans DIR (default .github/workflows, relative to the cwd — ul_setup cd's to the repo
#   root) for *.yml / *.yaml and looks at every `uses:` line of the form
#
#       uses: owner/repo[/path]@<40-hex> # <ref>
#
#   (an optional `- ` before `uses:`, optional single/double quotes around the value). <ref>
#   is the FIRST whitespace-separated word of the trailing comment: a tag or a branch name.
#   It is resolved to its CURRENT commit SHA and the SHA is rewritten in place; the comment
#   (including anything after the ref word) is preserved byte for byte. `path` may be a
#   reusable workflow (`owner/repo/.github/workflows/x.yml@sha # main`): only owner/repo is
#   used to resolve.
#
#   Lines that are NOT touched and NOT failures:
#     - local actions (`./...`), `docker://...`, anything not shaped owner/repo[/path]@ref,
#       and commented-out lines.
#     - a MUTABLE ref (`@v3`, `@main`: no SHA). It is REPORTED ("mutable ref", file:line),
#       never rewritten — unless --pin-mutable is given, which resolves the ref and rewrites
#       the line to `@<sha> # <ref>`. (A mutable line that already carries a comment is
#       reported but never pinned: there is no safe place to put the ref.)
#     - a SHA pin with no usable ref comment: REPORTED as "no ref comment", since there is
#       nothing to refresh it from.
#
#   FAILURES ARE NOT SWALLOWED. A ref that cannot be resolved (network, auth, unknown ref,
#   gh missing, a resolver answer that is not 40 hex) prints
#       cannot resolve <owner/repo>@<ref> (<file>:<line>): <resolver's own error>
#   on stderr and the function returns 3 after scanning EVERYTHING (so one run reports every
#   bad pin). The run is ALL-OR-NOTHING: when any pin failed, NO file is written, so a failed
#   step never leaves a partially refreshed set. Run under ul_run_step, exit 3 is a hard step
#   failure (rolled back, recorded, ul_finalize exits 1); the lib's stderr classifier may
#   instead call a pure transport error transient (retried next run), per ADR 0020.
#
#   Exit codes: 0 success (including "everything current" and "mutable refs reported");
#   2 usage error; 3 one or more refs could not be resolved. (1 stays the generic error.)
#
# RESOLUTION (backend selected by UL_ACTION_PINS_BACKEND = auto | gh | git; default auto =
# gh when it is on PATH, else git):
#   gh:  `gh api repos/<owner/repo>/commits/<ref> --jq .sha`. GitHub resolves the ref (tag or
#        branch) and returns the COMMIT; an annotated tag is peeled by the API. Uses gh's
#        own auth, so it also reaches private repos (e.g. a private reusable workflow).
#   git: `git ls-remote <base>/<owner/repo>.git refs/tags/<ref> refs/tags/<ref>^{} refs/heads/<ref>`
#        with <base> = $UL_ACTION_PINS_GIT_URL (default https://github.com). Precedence:
#        the PEELED line `refs/tags/<ref>^{}` (annotated tag -> the commit it points at),
#        else `refs/tags/<ref>` (lightweight tag: already a commit), else `refs/heads/<ref>`.
#        ANNOTATED TAGS ARE PEELED so the pin is the commit SHA `uses:` requires, never the
#        tag object's own SHA. A tag beats a same-named branch (git's own precedence).
#
# TEST SEAM: UL_ACTION_PINS_RESOLVER names a function/command called as
#   `<resolver> <owner/repo> <ref>` that prints the 40-hex SHA on stdout (default
#   _ul_ap_resolve_ref). Tests inject it or stub gh / point UL_ACTION_PINS_GIT_URL at local
#   repos, so the suite never touches the network.
#
# Portability: bash 3.2 compatible (no associative arrays, no ${var,,}); depends only on
# bash, coreutils, and git or gh.
#
# =================================================================

# Resolve <owner/repo> <ref> through `gh api`. Prints the commit SHA.
_ul_ap_resolve_gh() {
  local repo="$1" ref="$2"
  if ! command -v gh >/dev/null 2>&1; then
    echo "gh is not installed (UL_ACTION_PINS_BACKEND=gh)" >&2
    return 1
  fi
  gh api "repos/${repo}/commits/${ref}" --jq .sha
}

# Resolve <owner/repo> <ref> through `git ls-remote`, peeling annotated tags. Prints the
# commit SHA. See CONTRACT for the precedence.
_ul_ap_resolve_git() {
  local repo="$1" ref="$2"
  local base="${UL_ACTION_PINS_GIT_URL:-https://github.com}"
  local out sha name tag="" peeled="" head=""
  if ! out="$(GIT_TERMINAL_PROMPT=0 git ls-remote "${base%/}/${repo}.git" \
    "refs/tags/${ref}" "refs/tags/${ref}^{}" "refs/heads/${ref}")"; then
    echo "git ls-remote ${base%/}/${repo}.git failed" >&2
    return 1
  fi
  while read -r sha name; do
    case "$name" in
    "refs/tags/${ref}^{}") peeled="$sha" ;;
    "refs/tags/${ref}") tag="$sha" ;;
    "refs/heads/${ref}") head="$sha" ;;
    esac
  done <<<"$out"
  if [[ -n $peeled ]]; then
    echo "$peeled"
  elif [[ -n $tag ]]; then
    echo "$tag"
  elif [[ -n $head ]]; then
    echo "$head"
  else
    echo "no tag or branch named '${ref}' in ${repo}" >&2
    return 1
  fi
}

# Default resolver: dispatch on UL_ACTION_PINS_BACKEND.
_ul_ap_resolve_ref() {
  case "${UL_ACTION_PINS_BACKEND:-auto}" in
  gh) _ul_ap_resolve_gh "$@" ;;
  git) _ul_ap_resolve_git "$@" ;;
  auto)
    if command -v gh >/dev/null 2>&1; then
      _ul_ap_resolve_gh "$@"
    else
      _ul_ap_resolve_git "$@"
    fi
    ;;
  *)
    echo "unknown UL_ACTION_PINS_BACKEND '${UL_ACTION_PINS_BACKEND}' (want auto|gh|git)" >&2
    return 1
    ;;
  esac
}

# Per-run resolution cache: newline-separated "<owner/repo>@<ref><TAB>ok|err<TAB><value>".
_UL_AP_CACHE=""

# _ul_ap_lookup <owner/repo> <ref>: sets _UL_AP_RESULT to the lowercase 40-hex SHA (return 0)
# or to the error text (return 1). Each distinct repo@ref is resolved once per run, failures
# included.
_ul_ap_lookup() {
  local repo="$1" ref="$2" key status value errf rc=0 sha
  key="${repo}@${ref}"
  local tab=$'\t'
  while IFS="$tab" read -r k status value; do
    if [[ $k == "$key" ]]; then
      _UL_AP_RESULT="$value"
      [[ $status == ok ]]
      return
    fi
  done <<<"$_UL_AP_CACHE"

  errf="$(mktemp)"
  sha="$("${UL_ACTION_PINS_RESOLVER:-_ul_ap_resolve_ref}" "$repo" "$ref" 2>"$errf")" || rc=$?
  if [[ $rc -eq 0 && $sha =~ ^[0-9a-fA-F]{40}$ ]]; then
    status=ok
    value="$(printf '%s' "$sha" | tr '[:upper:]' '[:lower:]')"
  else
    status=err
    if [[ $rc -eq 0 ]]; then
      value="resolver returned '${sha}', not a 40-hex commit SHA"
    else
      value="$(tr '\n' ' ' <"$errf")"
      value="${value:-resolver exited ${rc} with no message}"
    fi
  fi
  rm -f "$errf"
  _UL_AP_CACHE+="${key}${tab}${status}${tab}${value}"$'\n'
  _UL_AP_RESULT="$value"
  [[ $status == ok ]]
}

# Scan + rewrite. $1 scratch dir, $2 workflows dir, $3 pin_mutable (true|false).
_ul_ap_run() {
  local tmp="$1" wf_dir="$2" pin_mutable="$3"
  local sq="'" dq='"'
  local re="^([[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+)(${dq}?|${sq}?)([^@[:space:]${dq}${sq}]+)@([^[:space:]${dq}${sq}#]+)(${dq}?|${sq}?)(([[:space:]]*)#[[:space:]]*(.*))?[[:space:]]*\$"
  local sha_re='^[0-9a-fA-F]{40}$'
  local act_re='^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/.*)?$'
  local ref_re='^[A-Za-z0-9][A-Za-z0-9._/-]*$'

  local updated=0 mutable=0 unrefreshable=0 failed=0 scanned=0
  local n=0 f out line lineno changed new_line
  local -a files=() outs=() flags=()

  _UL_AP_CACHE=""
  for f in "$wf_dir"/*.yml "$wf_dir"/*.yaml; do
    [[ -f $f ]] || continue
    n=$((n + 1))
    out="${tmp}/${n}"
    : >"$out"
    changed=false
    lineno=0
    while IFS= read -r line || [[ -n $line ]]; do
      lineno=$((lineno + 1))
      new_line="$line"
      if [[ $line =~ $re ]]; then
        local prefix="${BASH_REMATCH[1]}" q1="${BASH_REMATCH[3]}" action="${BASH_REMATCH[4]}"
        local ref="${BASH_REMATCH[5]}" q2="${BASH_REMATCH[6]}" has_comment="${BASH_REMATCH[7]}"
        local comment="${BASH_REMATCH[9]}"
        if [[ $action =~ $act_re ]]; then
          scanned=$((scanned + 1))
          local owner="${action%%/*}" rest="${action#*/}"
          local repo="${owner}/${rest%%/*}"
          local where="${f}:${lineno}"
          if [[ $ref =~ $sha_re ]]; then
            # SHA pin: the ref lives in the trailing comment (first word).
            local cref="${comment%%[[:space:]]*}"
            if [[ -z $cref || ! $cref =~ $ref_re ]]; then
              echo "  ? ${where}: ${action}@${ref:0:7}: pinned by SHA but has no ref comment (# <tag-or-branch>); cannot refresh" >&2
              unrefreshable=$((unrefreshable + 1))
            elif _ul_ap_lookup "$repo" "$cref"; then
              local newsha="$_UL_AP_RESULT" oldlc
              oldlc="$(printf '%s' "$ref" | tr '[:upper:]' '[:lower:]')"
              if [[ $newsha != "$oldlc" ]]; then
                new_line="${line/"@${ref}"/"@${newsha}"}"
                changed=true
                updated=$((updated + 1))
                echo "  ⬆ ${where}: ${repo}@${cref} ${ref:0:7} → ${newsha:0:7}"
              fi
            else
              echo "  ✗ cannot resolve ${repo}@${cref} (${where}): ${_UL_AP_RESULT}" >&2
              failed=$((failed + 1))
            fi
          else
            # Mutable ref (@v3, @main): report; pin only on request.
            mutable=$((mutable + 1))
            if [[ $pin_mutable != true ]]; then
              echo "  ! ${where}: ${action}@${ref}: mutable ref (no SHA) — not rewritten; pass --pin-mutable to pin it"
            elif [[ -n $has_comment ]]; then
              echo "  ! ${where}: ${action}@${ref}: mutable ref carries a comment already — not pinned (no safe place for the ref)"
            elif _ul_ap_lookup "$repo" "$ref"; then
              new_line="${prefix}${q1}${action}@${_UL_AP_RESULT}${q2} # ${ref}"
              changed=true
              updated=$((updated + 1))
              echo "  ⬆ ${where}: ${repo}@${ref} pinned to ${_UL_AP_RESULT:0:7}"
            else
              echo "  ✗ cannot resolve ${repo}@${ref} (${where}): ${_UL_AP_RESULT}" >&2
              failed=$((failed + 1))
            fi
          fi
        fi
      fi
      printf '%s\n' "$new_line" >>"$out"
    done <"$f"
    files+=("$f")
    outs+=("$out")
    flags+=("$changed")
  done

  if [[ $failed -gt 0 ]]; then
    echo "ul_refresh_action_pins: ${failed} ref(s) could not be resolved — no workflow file was modified" >&2
    return 3
  fi

  local i
  for ((i = 0; i < ${#files[@]}; i++)); do
    if [[ ${flags[$i]} == true ]]; then
      cat "${outs[$i]}" >"${files[$i]}"
    fi
  done
  echo "ul_refresh_action_pins: ${scanned} action ref(s) scanned, ${updated} updated, ${mutable} mutable (reported or pinned), ${unrefreshable} without a ref comment"
  return 0
}

ul_refresh_action_pins() {
  local pin_mutable=false wf_dir=".github/workflows"
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --pin-mutable) pin_mutable=true ;;
    --workflows-dir)
      if [[ $# -lt 2 ]]; then
        echo "ul_refresh_action_pins: --workflows-dir needs a directory" >&2
        return 2
      fi
      wf_dir="$2"
      shift
      ;;
    *)
      echo "ul_refresh_action_pins: unknown option: $1 (usage: ul_refresh_action_pins [--pin-mutable] [--workflows-dir DIR])" >&2
      return 2
      ;;
    esac
    shift
  done

  if [[ ! -d $wf_dir ]]; then
    echo "ul_refresh_action_pins: no ${wf_dir} directory — nothing to do"
    return 0
  fi

  local tmp rc=0
  tmp="$(mktemp -d)" || return 1
  _ul_ap_run "$tmp" "$wf_dir" "$pin_mutable" || rc=$?
  rm -rf "$tmp"
  return "$rc"
}
