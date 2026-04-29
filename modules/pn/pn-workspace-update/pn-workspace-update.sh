# shellcheck shell=bash
# pn-workspace-update: Update all flake dependencies

_root_arg=""
_workspace_arg=""
_override_specs=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-update: Update all flake dependencies

Purpose: Updates all flake dependencies (lock files) for every workspace repo
without applying changes. For each project in order, it pulls the latest remote
changes, runs update-locks.sh, and pushes. After all projects are updated,
regenerates the pn-workspace.lock file.

Projects without a configured upstream are still refreshed locally
(update-locks.sh runs); pull and push are skipped with an informational
message.

Usage: pn-workspace-update [OPTIONS]

Options:
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Updates run in the swapped path.
                                Also accepts PN_WORKSPACE_OVERRIDE_PATHS env var
                                with comma-separated entries.

Example:
  # Update all flake dependencies
  pn-workspace-update

  # Typically followed by pn-workspace-apply to apply the updates
  pn-workspace-update && pn-workspace-apply
HELP
    exit 0
    ;;
  --root)
    _root_arg="$2"
    shift 2
    ;;
  --root=*)
    _root_arg="${1#*=}"
    shift
    ;;
  --workspace)
    _workspace_arg="$2"
    shift 2
    ;;
  --workspace=*)
    _workspace_arg="${1#*=}"
    shift
    ;;
  --override-path)
    _override_specs+=("$2")
    shift 2
    ;;
  --override-path=*)
    _override_specs+=("${1#*=}")
    shift
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n $_root_arg && -n $_workspace_arg ]]; then
  echo "error: --root and --workspace are mutually exclusive (use --root)" >&2
  exit 1
fi

if [[ -n $_workspace_arg ]]; then
  echo "warning: --workspace is deprecated; use --root instead" >&2
  _root_arg="$_workspace_arg"
fi

PN_WORKSPACE_ROOT=$(workspace_resolve_root "$_root_arg") || exit 1

overrides_json=$(workspace_parse_overrides "${_override_specs[@]}") || exit 1

_child_pid=""
_current_project=""

_cleanup() {
  local signal="$1"
  if [[ -n $_child_pid ]] && kill -0 "$_child_pid" 2>/dev/null; then
    kill -TERM "$_child_pid" 2>/dev/null
    wait "$_child_pid" 2>/dev/null || true
  fi
  if [[ -n $_current_project ]]; then
    echo "" >&2
    echo "Interrupted during: $_current_project" >&2
  fi
  trap - "$signal" EXIT
  exit $((128 + $(kill -l "$signal")))
}
trap '_cleanup INT' INT
trap '_cleanup TERM' TERM

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || exit 1

  if workspace_has_upstream; then
    git pull --rebase --autostash &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  fi

  ./update-locks.sh &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  if workspace_has_upstream; then
    git push &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  else
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
  fi

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

# Regenerate lock file so pn-workspace.lock reflects any repos added since last update
echo "  --== Regenerating workspace lock ==--  "
pn-discover-workspace "$PN_WORKSPACE_ROOT" >"$PN_WORKSPACE_ROOT/pn-workspace.lock"
echo
