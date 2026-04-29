# shellcheck shell=bash
# pn-workspace-apply: Format and apply workspace configuration

_workspace_arg=""
_apply_cmd_arg=""
_terminal_path_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-apply: Format and apply workspace configuration

Purpose: This is the main command for applying configuration changes during
development. It formats all Nix files in the terminal flake, then rebuilds and
activates the configuration using the apply_command from pn-workspace.toml.
Non-terminal workspace repos are passed as --override-input arguments so local
changes are picked up without committing.

Usage: pn-workspace-apply [OPTIONS]

Options:
  -h, --help                  Show this help message and exit
  --workspace <dir>           Workspace root directory (default: walk up from CWD)
  --apply-cmd <template>      Override apply_command from pn-workspace.toml.
                              Supports {terminal_flake} and {hostname} placeholders.
  --terminal-path <path>      Override the terminal flake path from workspace discovery.

Example:
  # Apply configuration after making changes
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
  --apply-cmd)
    _apply_cmd_arg="$2"
    shift 2
    ;;
  --apply-cmd=*)
    _apply_cmd_arg="${1#*=}"
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

_apply_child_pid=""

_apply_cleanup() {
  local signal="$1"
  if [[ -n $_apply_child_pid ]] && kill -0 "$_apply_child_pid" 2>/dev/null; then
    sudo kill -TERM "$_apply_child_pid" 2>/dev/null || kill -TERM "$_apply_child_pid" 2>/dev/null
    wait "$_apply_child_pid" 2>/dev/null || true
  fi
  echo "" >&2
  echo "Interrupted during apply." >&2
  trap - "$signal" EXIT
  exit $((128 + $(kill -l "$signal")))
}
trap '_apply_cleanup INT' INT
trap '_apply_cleanup TERM' TERM

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

# Load update-cache-lib if available in any workspace project
UL_LIB=""
while IFS= read -r proj_path; do
  candidate="$proj_path/lib/scripts/update-cache-lib.bash"
  if [[ -f $candidate ]]; then
    UL_LIB="$candidate"
    break
  fi
done < <(echo "$workspace_json" | jq -r '.[] | .path')

if [[ -n $UL_LIB ]]; then
  # shellcheck disable=SC1090
  source "$UL_LIB"
  ul_init "apply"
  ul_check_nix_daemon
else
  ul_should_run() { return 0; }
  ul_mark_done() { :; }
  ul_needs_rebuild() { return 0; }
  ul_mark_applied() { :; }
  ul_check_nix_daemon() { :; }
fi

# Read apply_command template and substitute placeholders
if [[ -n $_apply_cmd_arg ]]; then
  apply_cmd_template="$_apply_cmd_arg"
elif [[ -f "$PN_WORKSPACE_ROOT/pn-workspace.toml" ]]; then
  apply_cmd_template=$(workspace_read_toml "$PN_WORKSPACE_ROOT" "apply_command")
else
  echo "error: no --apply-cmd given and no pn-workspace.toml at $PN_WORKSPACE_ROOT" >&2
  exit 1
fi

hostname_short=$(hostname -s)
apply_cmd="${apply_cmd_template/\{terminal_flake\}/$terminal_path}"
apply_cmd="${apply_cmd/\{hostname\}/$hostname_short}"

# Run pre_apply_hooks
while IFS= read -r hook; do
  [[ -z $hook || $hook == "null" ]] && continue
  echo "  --== Running pre-apply hook: $hook ==--  "
  $hook
  echo
done < <(workspace_read_toml "$PN_WORKSPACE_ROOT" "pre_apply_hooks[]" 2>/dev/null || true)

echo "  --== Formatting flake ==--  "
cd "$terminal_path" || exit 1
nix fmt
echo

echo "  --== Applying flake ==--  "

# Collect all workspace paths for ul_needs_rebuild
mapfile -t all_paths < <(echo "$workspace_json" | jq -r '.[] | .path')

if ul_needs_rebuild "${all_paths[@]}"; then
  old_profile=$(readlink /nix/var/nix/profiles/system)

  # Split the substituted apply_cmd into an array and append overrides
  read -ra apply_args <<<"$apply_cmd"
  "${apply_args[@]}" "${overrides[@]}" &
  _apply_child_pid=$!
  wait "$_apply_child_pid" || exit $?
  _apply_child_pid=""

  new_profile=$(readlink /nix/var/nix/profiles/system)
  if [[ $old_profile != "$new_profile" ]]; then
    echo
    echo "  --== Package changes ==--  "
    nvd diff "/nix/var/nix/profiles/$old_profile" "/nix/var/nix/profiles/$new_profile"
  fi

  ul_mark_applied "${all_paths[@]}"
fi
echo

# Run post_apply_hooks
while IFS= read -r hook; do
  [[ -z $hook || $hook == "null" ]] && continue
  echo "  --== Running post-apply hook: $hook ==--  "
  if ul_should_run "$hook"; then
    $hook
    ul_mark_done "$hook"
  fi
  echo
done < <(workspace_read_toml "$PN_WORKSPACE_ROOT" "post_apply_hooks[]" 2>/dev/null || true)
