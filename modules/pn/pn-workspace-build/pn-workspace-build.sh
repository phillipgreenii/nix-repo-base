# shellcheck shell=bash
# pn-workspace-build: Format and build workspace configuration without activating

_workspace_arg=""
_terminal_path_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-build: Format and build workspace configuration without activating

Purpose: This command validates configuration changes by formatting and building
the terminal flake without activating it. Useful when running in sandboxed
environments or when you want to verify changes before applying them. If the build
succeeds, you can manually apply with 'pn-workspace-apply'.

Usage: pn-workspace-build [OPTIONS]

Options:
  -h, --help                  Show this help message and exit
  --workspace <dir>           Workspace root directory (default: walk up from CWD)
  --terminal-path <path>      Override the terminal flake path from workspace discovery.

Example:
  # Build configuration to verify changes
  pn-workspace-build

  # If successful, manually apply
  pn-workspace-apply
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
  --terminal-path)
    _terminal_path_arg="$2"
    shift 2
    ;;
  --terminal-path=*)
    _terminal_path_arg="${1#*=}"
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

# The terminal flake is the entry with no inputName field
if [[ -n $_terminal_path_arg ]]; then
  terminal_path="$(cd "$_terminal_path_arg" 2>/dev/null && pwd)" || {
    echo "error: terminal-path directory not found: $_terminal_path_arg" >&2
    exit 1
  }
else
  terminal_path=$(echo "$workspace_json" | jq -r '.[] | select(.inputName == null) | .path' | tail -1)
fi

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
