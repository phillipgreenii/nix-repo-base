# shellcheck shell=bash
# pn-workspace-push: Push all workspace repos to their remotes

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-push: Push all workspace repos to their remotes

Purpose: Pushes every repo declared in the nearest pn-workspace.toml to its
remote. Searches ancestor directories from the current working directory to
find the workspace root.

Usage: pn-workspace-push [OPTIONS]

Options:
  -h, --help     Show this help message and exit

Example:
  # Push all workspace repos
  pn-workspace-push
HELP
  exit 0
fi

PN_WORKSPACE_ROOT=$(require_workspace_root) || exit 1

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Push $project_name ==--  "
  cd "$project_path" || exit 1
  git push
done < <(echo "$workspace_json" | jq -r '.[] | .path')
