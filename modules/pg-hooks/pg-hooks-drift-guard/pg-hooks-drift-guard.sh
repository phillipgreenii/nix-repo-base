# shellcheck shell=bash

# pg-hooks-drift-guard: fail when a source tree reintroduces the hook machinery
# that the per-clone hook bundle replaced (design: per-clone hook bundle,
# sections 7.1 step 6 and 8 "drift guard"; ADR 0032).
#
# It reports, outside an explicit allowlist, any of:
#   githooks-dir        a `.githooks` directory
#   githooks-ref        text that names `.githooks`
#   relative-hooks-path text that sets core.hooksPath to a relative value
#   config-link         link code or text for `.pre-commit-config.yaml`
#   removed-machinery   an identifier of the deleted shim / link / higher-scope
#                       hooksPath code (spec 7.5)
#
# The allowlist is data, never a loosened pattern: one
# `<path-glob><TAB><categories><TAB><reason>` line per entry. `categories` is a
# comma list of the names above, or `*` for all of them; the reason is mandatory.
# A glob is matched against the repo-relative path with `case` (`*` also crosses
# `/`), so an entry allows only the named categories in the matching files.
#
# Exit codes: 0 clean, 2 usage or a bad allowlist, 10 drift found, 1 unexpected.
# BASH 3.2 STYLE (no mapfile, no associative arrays) like the rest of pg-hooks.

show_help() {
  cat <<'HELP'
pg-hooks-drift-guard: fail on hook-machinery drift outside an explicit allowlist

Usage: pg-hooks-drift-guard --root <dir> [--allowlist <file>]

Options:
  --root <dir>         The source tree to scan (a repo root or a flake source)
  --allowlist <file>   One "<path-glob><TAB><categories><TAB><reason>" per line
                       (categories: a comma list, or * for all); "#" and blank
                       lines are ignored; the reason is mandatory. The file
                       itself is never scanned.
  -h, --help           Show this help message
  -v, --version        Show version information

Categories: githooks-dir, githooks-ref, relative-hooks-path, config-link,
removed-machinery. Directories named .git, .worktrees and .workforests are
skipped.

Exit codes:
  0   no drift
  2   usage error or a malformed allowlist
  10  drift found (each finding is printed on stderr)
  1   unexpected failure
HELP
}

die() {
  printf 'pg-hooks-drift-guard: %s\n' "$1" >&2
  exit "${2:-1}"
}

# Anything that is not an explicit result (0, 2, 10) is "unexpected" (1).
# shellcheck disable=SC2329  # invoked through the EXIT trap below
on_exit() {
  local rc=$?
  if ((rc != 0 && rc != 2 && rc != 10)); then
    exit 1
  fi
}
trap on_exit EXIT

ROOT=""
ALLOWLIST=""
while (($# > 0)); do
  case "$1" in
  --root)
    (($# >= 2)) || die "--root needs a directory" 2
    ROOT=$2
    shift 2
    ;;
  --allowlist)
    (($# >= 2)) || die "--allowlist needs a file" 2
    ALLOWLIST=$2
    shift 2
    ;;
  -h | --help)
    show_help
    exit 0
    ;;
  *)
    die "unknown argument: $1 (see --help)" 2
    ;;
  esac
done

[[ -n $ROOT ]] || die "--root is required (see --help)" 2
[[ -d $ROOT ]] || die "--root is not a directory: $ROOT" 2
if [[ -n $ALLOWLIST ]]; then
  [[ -f $ALLOWLIST ]] || die "--allowlist is not a file: $ALLOWLIST" 2
fi

# Resolve the allowlist to an absolute path before changing directory, and note
# its repo-relative name so the scan can skip it.
ALLOW_ABS=""
if [[ -n $ALLOWLIST ]]; then
  ALLOW_ABS="$(cd "$(dirname "$ALLOWLIST")" && pwd -P)/$(basename "$ALLOWLIST")"
fi
ROOT_ABS="$(cd "$ROOT" && pwd -P)"
cd "$ROOT_ABS" || die "cannot enter $ROOT_ABS"
SELF_REL=""
if [[ -n $ALLOW_ABS && $ALLOW_ABS == "$ROOT_ABS"/* ]]; then
  SELF_REL="${ALLOW_ABS#"$ROOT_ABS"/}"
fi

ALLOW_PATTERNS=()
ALLOW_CATEGORIES=()
ALLOW_REASONS=()
ALLOW_USED=()
if [[ -n $ALLOW_ABS ]]; then
  lineno=0
  while IFS= read -r line || [[ -n $line ]]; do
    lineno=$((lineno + 1))
    [[ -z ${line//[[:space:]]/} || $line == \#* ]] && continue
    IFS=$'\t' read -r pat cats reason <<<"$line"
    if [[ -z ${pat//[[:space:]]/} || -z ${cats//[[:space:]]/} || -z ${reason//[[:space:]]/} ]]; then
      die "$ALLOWLIST:$lineno: an allowlist line is '<path-glob><TAB><categories><TAB><reason>', all three mandatory" 2
    fi
    ALLOW_PATTERNS+=("$pat")
    ALLOW_CATEGORIES+=(",${cats//[[:space:]]/},")
    ALLOW_REASONS+=("$reason")
    ALLOW_USED+=(0)
  done <"$ALLOW_ABS"
fi

# allowed <rel-path> <category>: true (and marks the entry used) when an
# allowlist glob covers the path for that category. The first match wins.
allowed() {
  local path=$1 category=$2 i=0 n=${#ALLOW_PATTERNS[@]}
  while ((i < n)); do
    if [[ ${ALLOW_CATEGORIES[$i]} == ',*,' || ${ALLOW_CATEGORIES[$i]} == *",$category,"* ]]; then
      # shellcheck disable=SC2254  # the allowlist glob is meant to be a pattern
      case "$path" in
      ${ALLOW_PATTERNS[$i]})
        ALLOW_USED[i]=1
        return 0
        ;;
      esac
    fi
    i=$((i + 1))
  done
  return 1
}

FINDINGS=0
report() {
  printf 'pg-hooks-drift-guard: %s: %s\n' "$1" "$2" >&2
  FINDINGS=$((FINDINGS + 1))
}

PRUNE=(--exclude-dir=.git --exclude-dir=.worktrees --exclude-dir=.workforests)

# scan <category> <grep -E pattern> [-i]: one recursive pass; every hit whose
# file is not allowlisted is a finding. -I skips binary files and -r does not
# follow symlinks.
scan() {
  local category=$1 pattern=$2 flag=${3:--E}
  local rec rel rest
  while IFS= read -r rec; do
    rec=${rec#./}
    rel=${rec%%:*}
    rest=${rec#*:}
    [[ -n $SELF_REL && $rel == "$SELF_REL" ]] && continue
    allowed "$rel" "$category" && continue
    report "$category" "$rel:$rest"
  done < <(grep -r -I -n -E "${PRUNE[@]}" "$flag" -e "$pattern" . 2>/dev/null || true)
}

# githooks-dir
while IFS= read -r d; do
  d=${d#./}
  allowed "$d" githooks-dir && continue
  report githooks-dir "$d"
done < <(find . \( -name .git -o -name .worktrees -o -name .workforests \) -prune -o -type d -name .githooks -print)

scan githooks-ref '\.githooks'

# A relative value: separated from core.hooksPath by whitespace or "=", then
# "./", "../", a dot-name (".githooks") or "dir/". An absolute path, "$VAR" or
# "~" does not match, and neither does ordinary prose ("core.hooksPath is ...").
scan relative-hooks-path "core\\.hooksPath([[:space:]]*=[[:space:]]*|[[:space:]]+)[\"']?(\\./|\\.\\./|\\.[A-Za-z]|[A-Za-z0-9_-]+/)"

# Link code or text: a line naming the generated config together with a link
# verb, in either order (case-insensitive: "Symlink(", "symlink", "linked").
scan config-link 'pre-commit-config.*(ln -[a-z]*s|symlink|readlink|(^|[^a-z])link)|(ln -[a-z]*s|symlink|readlink|(^|[^a-z])link).*pre-commit-config' -i

# Identifiers of deleted code (spec 7.5 and the T21 deletions).
scan removed-machinery 'commitTimeShim|shimLinkConfig|shimWireHooksPath|gitHookPackage|neutralizeHigherScopeHooksPath|restoreHigherScopeHooksPath|correctRelativeHooksPath|hardenPrePushHook|absolutizeHookConfigPath|linkPreCommitConfig|symlinkLiveInNixStore|HookBundleLegacy|PREK_ALLOW_NO_CONFIG|pre-commit-githooks-wired|pre-commit-hooks-path-worktree-safe|pre-commit-hooks-config-path-absolute'

# A stale allowlist entry is a notice, never a failure: the file it named may
# have been legitimately cleaned up.
i=0
while ((i < ${#ALLOW_PATTERNS[@]})); do
  if ((ALLOW_USED[i] == 0)); then
    printf 'pg-hooks-drift-guard: note: allowlist entry matched nothing: %s\n' "${ALLOW_PATTERNS[$i]}" >&2
  fi
  i=$((i + 1))
done

if ((FINDINGS > 0)); then
  printf 'pg-hooks-drift-guard: %d finding(s) outside the allowlist; remove the drift, or add a path and reason to the allowlist if it is intentional\n' "$FINDINGS" >&2
  exit 10
fi
exit 0
