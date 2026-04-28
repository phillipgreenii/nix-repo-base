# shellcheck shell=bash
# pn-workspace-rebase: Rebase all workspace repos with remote changes

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-rebase: Rebase all workspace repos with remote changes

Purpose: Runs 'git mu' (custom git alias for maintenance/update) in every repo
declared in the nearest pn-workspace.toml. Searches ancestor directories from
the current working directory to find the workspace root.

Usage: pn-workspace-rebase [OPTIONS]

Options:
  -h, --help     Show this help message and exit

Example:
  # Rebase all workspace repos
  pn-workspace-rebase
HELP
  exit 0
fi

PN_WORKSPACE_ROOT=$(require_workspace_root) || exit 1

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Rebase $project_name ==--  "
  cd "$project_path" || exit 1
  git mu
done < <(echo "$workspace_json" | jq -r '.[] | .path')
