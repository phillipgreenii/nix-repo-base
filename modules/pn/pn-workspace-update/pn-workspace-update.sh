# shellcheck shell=bash
# pn-workspace-update: Update all flake dependencies

_workspace_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-update: Update all flake dependencies

Purpose: Updates all flake dependencies (lock files) for every workspace repo
without applying changes. For each project in order, it pulls the latest remote
changes, runs update-locks.sh, and pushes. After all projects are updated,
regenerates the pn-workspace.lock file.

Usage: pn-workspace-update [OPTIONS]

Options:
  -h, --help              Show this help message and exit
  --workspace <dir>       Workspace root directory (default: walk up from CWD)

Example:
  # Update all flake dependencies
  pn-workspace-update

  # Typically followed by pn-workspace-apply to apply the updates
  pn-workspace-update && pn-workspace-apply
HELP
    exit 0
    ;;
  --workspace)
    _workspace_arg="$2"
    shift 2
    ;;
  --workspace=*)
    _workspace_arg="${1#*=}"
    shift
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n $_workspace_arg ]]; then
  PN_WORKSPACE_ROOT="$(cd "$_workspace_arg" 2>/dev/null && pwd)" || {
    echo "error: workspace directory not found: $_workspace_arg" >&2
    exit 1
  }
else
  PN_WORKSPACE_ROOT=$(require_workspace_root) || exit 1
fi

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

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || exit 1

  git pull --rebase --autostash &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  ./update-locks.sh &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  git push &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

# Regenerate lock file so pn-workspace.lock reflects any repos added since last update
echo "  --== Regenerating workspace lock ==--  "
pn-discover-workspace "$PN_WORKSPACE_ROOT" >"$PN_WORKSPACE_ROOT/pn-workspace.lock"
echo
