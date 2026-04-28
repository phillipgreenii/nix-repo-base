# shellcheck shell=bash
# pn-workspace-build: Format and build workspace configuration without activating

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-build: Format and build workspace configuration without activating

Purpose: This command validates configuration changes by formatting and building
the terminal flake without activating it. Useful when running in sandboxed
environments or when you want to verify changes before applying them. If the build
succeeds, you can manually apply with 'pn-workspace-apply'.

Usage: pn-workspace-build [OPTIONS]

Options:
  -h, --help     Show this help message and exit

Example:
  # Build configuration to verify changes
  pn-workspace-build

  # If successful, manually apply
  pn-workspace-apply
HELP
  exit 0
fi

PN_WORKSPACE_ROOT=$(require_workspace_root) || exit 1

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT")

# The terminal flake is the entry with no inputName field
terminal_path=$(echo "$workspace_json" | jq -r '.[] | select(.inputName == null) | .path' | tail -1)

if [[ -z $terminal_path ]]; then
  echo "error: could not determine terminal flake path from workspace" >&2
  exit 1
fi

# Build --override-input args for all non-terminal repos
overrides=()
while IFS= read -r entry; do
  path=$(echo "$entry" | jq -r '.path')
  input_name=$(echo "$entry" | jq -r '.inputName')
  overrides+=(--override-input "$input_name" "git+file://$path")
done < <(echo "$workspace_json" | jq -c '.[] | select(.inputName != null)')

echo "  --== Formatting flake ==--  "
cd "$terminal_path" || exit 1
nix fmt
echo

echo "  --== Building flake ==--  "
darwin-rebuild build --flake "$terminal_path" "${overrides[@]}"
echo

echo "Build successful! To apply, run:"
echo "  pn-workspace-apply"
