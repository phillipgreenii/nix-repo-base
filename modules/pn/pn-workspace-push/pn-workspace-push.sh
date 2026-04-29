# shellcheck shell=bash
# pn-workspace-push: Push all workspace repos to their remotes

_workspace_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-push: Push all workspace repos to their remotes

Purpose: Pushes every repo declared in the nearest pn-workspace.toml to its
remote. Searches ancestor directories from the current working directory to
find the workspace root.

Usage: pn-workspace-push [OPTIONS]

Options:
  -h, --help              Show this help message and exit
  --workspace <dir>       Workspace root directory (default: walk up from CWD)

Example:
  # Push all workspace repos
  pn-workspace-push
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

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Push $project_name ==--  "
  cd "$project_path" || exit 1
  git push
done < <(echo "$workspace_json" | jq -r '.[] | .path')
