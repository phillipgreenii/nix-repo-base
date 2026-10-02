# shellcheck shell=bash

# pg-hooks-install: root a hook bundle under <git-common-dir>/pg-hooks/gen-N,
# swap the `current` pointer and write the static stubs (design: per-clone hook
# bundle, sections 4.2 generations and pruning, 4.4 installer, 6 failure modes).
# The builder prepends pg-hooks-lib.bash (pgh_*: git context, pointer, stamp).
#
# It is the body of `install-pre-commit-hooks` when `bundle.enable` is set:
#   exec pg-hooks-install --bundle <store path of the bundle> "$@"
# It MUST NOT call `nix build` (the bundle is already in the store because the
# wrapper's text embeds it) and MUST NOT write core.hooksPath.
#
# Exit codes: 0 installed, 2 refused or usage, 1 unexpected.
# BASH 3.2 STYLE (no mapfile, no associative arrays) like the rest of pg-hooks.

show_help() {
  cat <<'HELP'
pg-hooks-install: root a hook bundle in this clone and write the hook stubs

Usage: pg-hooks-install --bundle <store path> [--private] [--override <name>=<path>]...

Options:
  --bundle <path>          The built pg-hooks bundle (a /nix/store path)
  --private                In a linked worktree: install a worktree-only bundle
                           under the worktree's git dir (never touches the shared
                           bundle; may add a stub for a stage that has none)
  --override <name>=<path> Record a flake input override (repeatable); the path's
                           HEAD and dirty state go into source.json
  -h, --help               Show this help message
  -v, --version            Show version information

Exit codes:
  0  installed
  2  refused (linked worktree without --private, or a foreign hook file) or usage
  1  unexpected failure

Environment:
  PG_HOOKS_NIX_STORE_BIN   nix-store to run for --add-root (default: nix-store)
HELP
}

die() {
  printf 'pg-hooks: %s\n' "$1" >&2
  exit "${2:-1}"
}

# Anything that is not an explicit refusal (2) is "unexpected" (1), whatever the
# failing command's own status was.
on_exit() {
  local rc=$?
  if ((rc != 0 && rc != 2)); then
    exit 1
  fi
}
trap on_exit EXIT

# The stub template path is injected by the nix wrapper (config STUB_TEMPLATE);
# tests running the raw source pass it in the environment.
STUB_TEMPLATE_FILE=${STUB_TEMPLATE:-}
NIX_STORE_BIN=${PG_HOOKS_NIX_STORE_BIN:-nix-store}

# Client-side hook stages prek can install a stub for. A stage outside this list
# (prek's `manual`, a typo) never gets a file in the hooks directory.
HOOK_STAGES="pre-commit pre-merge-commit pre-push pre-rebase commit-msg prepare-commit-msg post-checkout post-commit post-merge post-rewrite"

BUNDLE=""
PRIVATE=0
OVERRIDES=()

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --bundle)
    [[ $# -ge 2 ]] || die "--bundle needs a path" 2
    BUNDLE=$2
    shift
    ;;
  --bundle=*) BUNDLE=${1#--bundle=} ;;
  --private) PRIVATE=1 ;;
  --override)
    [[ $# -ge 2 ]] || die "--override needs <name>=<path>" 2
    OVERRIDES+=("$2")
    shift
    ;;
  --override=*) OVERRIDES+=("${1#--override=}") ;;
  *) die "unknown argument: $1 (see --help)" 2 ;;
  esac
  shift
done

[[ -n $BUNDLE ]] || die "--bundle <path> is required" 2

# Normalise the overrides (name=path, absolute path when it exists).
NORMALISED=()
for ov in ${OVERRIDES[@]+"${OVERRIDES[@]}"}; do
  if [[ $ov != ?*=?* ]]; then
    die "--override expects <name>=<path>, got: $ov" 2
  fi
  ov_name=${ov%%=*}
  ov_path=${ov#*=}
  if [[ -d $ov_path ]]; then
    ov_path=$(cd "$ov_path" && pwd)
  fi
  NORMALISED+=("$ov_name=$ov_path")
done
OVERRIDES=()
for ov in ${NORMALISED[@]+"${NORMALISED[@]}"}; do
  OVERRIDES+=("$ov")
done

pgh_ctx --toplevel || die "not inside a git repository" 2

COMMON=$PGH_COMMON
GITDIR=$PGH_GITDIR
TOP=$PGH_TOP
CANONICAL=${COMMON%/*}
HOOKS_DIR=$COMMON/hooks

LINKED=0
if [[ $GITDIR != "$COMMON" ]]; then
  LINKED=1
fi

# Private mode only exists in a linked worktree. In the canonical clone the
# private and shared directories are the same directory, so --private there is a
# plain canonical install.
MODE=canonical
if ((LINKED)); then
  if ((PRIVATE)); then
    MODE=private
  fi
fi

# quote <string>: shell-quote only when needed (a plain path is unchanged).
quote() {
  printf '%q' "$1"
}

# build_reinstall: the exact command that rebuilds this install, as stored in
# the `reinstall` file and named in messages. Paths with a space are quoted.
build_reinstall() {
  local pins="" opts="" ov name path
  for ov in ${OVERRIDES[@]+"${OVERRIDES[@]}"}; do
    name=${ov%%=*}
    path=${ov#*=}
    pins="$pins --override-input $(quote "$name") $(quote "git+file://$path")"
    opts="$opts --override $(quote "$ov")"
  done
  if [[ $MODE == private ]]; then
    printf '(cd %s && nix run%s .#install-pre-commit-hooks -- --private%s)\n' \
      "$(quote "$TOP")" "$pins" "$opts"
  elif [[ -n $opts ]]; then
    printf '(cd %s && nix run%s .#install-pre-commit-hooks --%s)\n' \
      "$(quote "$CANONICAL")" "$pins" "$opts"
  else
    printf '(cd %s && nix run .#install-pre-commit-hooks)\n' "$(quote "$CANONICAL")"
  fi
}

if ((LINKED)) && [[ $MODE != private ]]; then
  printf 'pg-hooks: install refused in a linked worktree; the shared bundle belongs to %s. Run there: (cd %s && nix run .#install-pre-commit-hooks), or add --private for a worktree-only bundle.\n' \
    "$CANONICAL" "$(quote "$CANONICAL")" >&2
  exit 2
fi

[[ -d $BUNDLE ]] || die "bundle not found: $BUNDLE"
[[ -f $BUNDLE/stages.json ]] || die "bundle has no stages.json: $BUNDLE"
jq -e 'type == "object"' "$BUNDLE/stages.json" >/dev/null || die "bundle stages.json is not an object: $BUNDLE"
[[ -r $STUB_TEMPLATE_FILE ]] || die "stub template not readable: ${STUB_TEMPLATE_FILE:-<unset>}"

if [[ $MODE == private ]]; then
  PG_DIR=$GITDIR/pg-hooks
else
  PG_DIR=$COMMON/pg-hooks
fi
REINSTALL=$(build_reinstall)

# --- stages that need a stub -------------------------------------------------

# Stages with at least one configured hook (stages.json maps stage -> [hooks]),
# restricted to real client-side hook names.
NEEDED=()
while IFS= read -r stage; do
  [[ -n $stage ]] || continue
  for known in $HOOK_STAGES; do
    if [[ $known == "$stage" ]]; then
      NEEDED+=("$stage")
      break
    fi
  done
done < <(jq -r 'to_entries[] | select((.value | length) > 0) | .key' "$BUNDLE/stages.json")

is_needed() {
  local s
  for s in ${NEEDED[@]+"${NEEDED[@]}"}; do
    if [[ $s == "$1" ]]; then
      return 0
    fi
  done
  return 1
}

# stub_kind <file>: sets STUB_KIND to absent | own | legacy | foreign and
# STUB_FIRST to the file's first line. Own carries the marker line
# `# managed-by: pg-hooks`; legacy carries prek's `File generated by prek`.
stub_kind() {
  local f=$1 line i=0
  STUB_KIND=absent
  STUB_FIRST=""
  if [[ ! -e $f && ! -L $f ]]; then
    return 0
  fi
  STUB_KIND=foreign
  if [[ -f $f ]]; then
    {
      while ((i < 5)) && IFS= read -r line; do
        if ((i == 0)); then
          STUB_FIRST=$line
        fi
        i=$((i + 1))
        if [[ $line == '# managed-by: pg-hooks' ]]; then
          STUB_KIND=own
          break
        fi
        if [[ $line == *'File generated by prek'* ]]; then
          STUB_KIND=legacy
          break
        fi
      done
    } <"$f" 2>/dev/null || true
  fi
  return 0
}

# refuse_foreign: exit 2, before anything else is changed, when a foreign file
# occupies a needed stage.
refuse_foreign() {
  local s
  for s in ${NEEDED[@]+"${NEEDED[@]}"}; do
    stub_kind "$HOOKS_DIR/$s"
    if [[ $STUB_KIND == foreign ]]; then
      printf 'pg-hooks: install refused: %s is not managed by pg-hooks (first line: %s). Move it aside or chain it yourself, then rerun: %s\n' \
        "$HOOKS_DIR/$s" "$STUB_FIRST" "$REINSTALL" >&2
      exit 2
    fi
  done
}

if [[ $MODE == canonical ]]; then
  refuse_foreign
fi

# --- generation --------------------------------------------------------------

# gen_number <name>: print N for gen-N, else nothing.
gen_number() {
  local base=${1##*/}
  if [[ $base =~ ^gen-([0-9]+)$ ]]; then
    printf '%s\n' "$((10#${BASH_REMATCH[1]}))"
  fi
}

# claim_generation: mkdir gen-N for N = 1 + the largest existing gen; mkdir is
# the atomic claim, so an EEXIST (a concurrent install took N) retries with the
# next number. Sets GEN.
claim_generation() {
  local d num max n tries=0
  while :; do
    max=0
    for d in "$PG_DIR"/gen-*; do
      [[ -d $d ]] || continue
      num=$(gen_number "$d")
      if [[ -n $num ]] && ((num > max)); then
        max=$num
      fi
    done
    n=$((max + 1))
    if mkdir "$PG_DIR/gen-$n" 2>/dev/null; then
      GEN=gen-$n
      return 0
    fi
    if [[ ! -d $PG_DIR/gen-$n ]]; then
      die "cannot create $PG_DIR/gen-$n"
    fi
    tries=$((tries + 1))
    if ((tries > 200)); then
      die "cannot claim a generation under $PG_DIR"
    fi
  done
}

# write_source_json <gen dir>
write_source_json() {
  local gdir=$1 stamp built_at ov name path head dirty overrides='[]' tmp
  local -a inputs=(flake.lock flake.nix)
  local p
  while IFS= read -r p; do
    if [[ -n $p ]]; then
      inputs+=("$p")
    fi
  done < <(jq -r '.stampPaths[]?' "$BUNDLE/meta.json" 2>/dev/null || true)
  if [[ -n $TOP ]]; then
    stamp=$(
      cd "$TOP" || exit 1
      pgh_stamp "${inputs[@]}"
    )
  else
    stamp=unknown
  fi
  for ov in ${OVERRIDES[@]+"${OVERRIDES[@]}"}; do
    name=${ov%%=*}
    path=${ov#*=}
    head=$(git -C "$path" rev-parse HEAD 2>/dev/null) || head=""
    if [[ -n $(git -C "$path" status --porcelain 2>/dev/null) ]]; then
      dirty=true
    else
      dirty=false
    fi
    overrides=$(jq -c --arg n "$name" --arg p "$path" --arg h "$head" --argjson d "$dirty" \
      '. + [{name: $n, path: $p, head: $h, dirty: $d}]' <<<"$overrides")
  done
  built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  tmp=$(mktemp "$gdir/.source.XXXXXX")
  jq -n --arg stamp "$stamp" --argjson overrides "$overrides" \
    --arg clone_path "$COMMON" --arg built_at "$built_at" \
    '{stamp: $stamp, overrides: $overrides, clone_path: $clone_path, built_at: $built_at}' >"$tmp"
  chmod 644 "$tmp"
  mv -f "$tmp" "$gdir/source.json"
}

# install_generation: claim, root, record, swap. Sets RETRY=1 when a concurrent
# install pruned this generation before the pointer swap landed (call it again).
# It is called as a plain command, never in an `||` list, so errexit stays
# active inside it.
install_generation() {
  RETRY=0
  claim_generation
  local gdir=$PG_DIR/$GEN
  if ! "$NIX_STORE_BIN" --add-root "$gdir/bundle" --indirect --realise "$BUNDLE" >/dev/null; then
    rm -rf "$gdir"
    die "nix-store --add-root failed for $BUNDLE"
  fi
  if [[ ! -e $gdir/bundle/bin/pg-hooks-run ]]; then
    rm -rf "$gdir"
    die "bundle $BUNDLE has no bin/pg-hooks-run"
  fi
  write_source_json "$gdir"
  if ! pgh_write_pointer "$PG_DIR" "$GEN"; then
    rm -rf "$gdir"
    die "cannot write the pointer $PG_DIR/current"
  fi
  if [[ ! -f $gdir/source.json ]]; then
    RETRY=1
  fi
}

write_reinstall() {
  local tmp
  tmp=$(mktemp "$PG_DIR/.reinstall.XXXXXX")
  printf '%s\n' "$REINSTALL" >"$tmp"
  chmod 644 "$tmp"
  mv -f "$tmp" "$PG_DIR/reinstall"
}

# prune: keep the generation `current` names and the highest COMPLETE one below
# it; delete the other complete lower generations. Generations above `current`
# and incomplete ones (no source.json yet) may belong to a concurrent install
# that has not swapped yet, so they are left alone. Never touches current's
# target, `current`, `reinstall` or the stale-warned-<stamp> markers.
prune() {
  local cur curn d num prev=-1
  cur=$(pgh_read_pointer "$PG_DIR") || return 0
  curn=$((10#${cur#gen-}))
  for d in "$PG_DIR"/gen-*; do
    [[ -d $d ]] || continue
    num=$(gen_number "$d")
    [[ -n $num ]] || continue
    if ((num < curn)) && [[ -f $d/source.json ]] && ((num > prev)); then
      prev=$num
    fi
  done
  for d in "$PG_DIR"/gen-*; do
    [[ -d $d ]] || continue
    num=$(gen_number "$d")
    [[ -n $num ]] || continue
    if ((num < curn && num != prev)) && [[ -f $d/source.json ]]; then
      rm -rf "$d"
    fi
  done
}

# --- stubs -------------------------------------------------------------------

# write_stub <stage>: render the template (@STAGE@ -> stage) into a temp file in
# the hooks dir and rename it into place, so a concurrent hook never sees a
# partial stub.
write_stub() {
  local stage=$1 content tmp
  content=$(<"$STUB_TEMPLATE_FILE")
  content=${content//@STAGE@/$stage}
  mkdir -p "$HOOKS_DIR"
  tmp=$(mktemp "$HOOKS_DIR/.pg-hooks-stub.XXXXXX")
  printf '%s\n' "$content" >"$tmp"
  chmod 755 "$tmp"
  mv -f "$tmp" "$HOOKS_DIR/$stage"
}

sync_stubs() {
  local s
  if [[ $MODE == private ]]; then
    # A set's new stage must work: add a missing stub, never rewrite or remove one.
    for s in ${NEEDED[@]+"${NEEDED[@]}"}; do
      stub_kind "$HOOKS_DIR/$s"
      if [[ $STUB_KIND == absent ]]; then
        write_stub "$s"
      fi
    done
    return 0
  fi
  refuse_foreign
  for s in ${NEEDED[@]+"${NEEDED[@]}"}; do
    write_stub "$s"
  done
  for s in $HOOK_STAGES; do
    if is_needed "$s"; then
      continue
    fi
    stub_kind "$HOOKS_DIR/$s"
    if [[ $STUB_KIND == own ]]; then
      rm -f "$HOOKS_DIR/$s"
    fi
  done
}

# --- main --------------------------------------------------------------------

mkdir -p "$PG_DIR"
GEN=""
attempt=0
RETRY=1
while ((RETRY)); do
  attempt=$((attempt + 1))
  if ((attempt > 5)); then
    die "cannot install a generation under $PG_DIR"
  fi
  install_generation
done
write_reinstall
prune
sync_stubs
