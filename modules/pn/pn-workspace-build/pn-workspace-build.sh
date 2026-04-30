# shellcheck shell=bash
# pn-workspace-build: Format and build workspace configuration without activating

_root_arg=""
_workspace_arg=""
_override_specs=()
_show_nix_commands_only=false

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
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Both terminal and non-terminal
                                projects supported. <name> is the workspace
                                directory name (e.g., "phillipg-nix-repo-base").
                                Also accepts PN_WORKSPACE_OVERRIDE_PATHS env var
                                with comma-separated entries.
  --show-nix-commands-only      Print nix commands in execution order and exit.
                                Does not format or build anything.

Example:
  # Build configuration to verify changes
  pn-workspace-build

  # Build using a worktree of repo-base
  pn-workspace-build --override-path repo-base=/path/to/worktree

  # If successful, manually apply
  pn-workspace-apply
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
  --show-nix-commands-only)
    _show_nix_commands_only=true
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

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

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

if [[ $_show_nix_commands_only == true ]]; then
  echo "cd $terminal_path && nix fmt"
  echo "darwin-rebuild build --flake $terminal_path ${overrides[*]}"
  exit 0
fi

echo "  --== Formatting flake ==--  "
cd "$terminal_path" || exit 1
nix fmt
echo

echo "  --== Building flake ==--  "
darwin-rebuild build --flake "$terminal_path" "${overrides[@]}"
echo

echo "Build successful! To apply, run:"
echo "  pn-workspace-apply"
