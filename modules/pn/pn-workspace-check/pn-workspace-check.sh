# shellcheck shell=bash
# pn-workspace-check: Run pre-commit checks for all workspace repos

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-check: Run pre-commit checks for all workspace repos

Purpose: Runs pre-commit checks (formatting, linting, tests) for every repo
declared in the nearest pn-workspace.toml. Searches ancestor directories from
the current working directory to find the workspace root.

Usage: pn-workspace-check [OPTIONS]

Options:
  -h, --help     Show this help message and exit

Example:
  # Run pre-commit checks for all workspace repos
  pn-workspace-check
HELP
  exit 0
fi

PN_WORKSPACE_ROOT=$(require_workspace_root) || exit 1

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Check $project_name ==--  "
  cd "$project_path" || exit 1
  pre-commit run --all-files
  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
