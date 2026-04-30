# shellcheck shell=bash
# pn-workspace-apply: Format and apply workspace configuration

_root_arg=""
_workspace_arg=""
_apply_cmd_arg=""
_override_specs=()
_show_nix_commands_only=false

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
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --apply-cmd <template>        Override apply_command from pn-workspace.toml.
                                Supports {terminal_flake} and {hostname} placeholders.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Both terminal and non-terminal
                                projects supported. Also accepts
                                PN_WORKSPACE_OVERRIDE_PATHS env var.
  --show-nix-commands-only      Print nix commands in execution order and exit.
                                Does not format, check daemon, or apply anything.

Example:
  # Apply configuration after making changes
  pn-workspace-apply

  # Bootstrap: run directly from nix-repo-base without pn installed
  nix run /path/to/nix-repo-base#pn-workspace-apply -- \
    --root ~/my-workspace \
    --apply-cmd "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
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
  --apply-cmd)
    _apply_cmd_arg="$2"
    shift 2
    ;;
  --apply-cmd=*)
    _apply_cmd_arg="${1#*=}"
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

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

# The terminal flake is the entry with no inputName field
terminal_path=$(echo "$workspace_json" | jq -r '.[] | select(.inputName == null) | .path' | tail -1)

if [[ -z $terminal_path ]]; then
  echo "error: could not determine terminal flake path from workspace" >&2
  exit 1
fi

workspace_check_follows "$terminal_path" "$workspace_json" || exit 1

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

if [[ -n $UL_LIB && $_show_nix_commands_only == false ]]; then
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

if [[ $_show_nix_commands_only == true ]]; then
  read -ra apply_args <<<"$apply_cmd"
  echo "cd $terminal_path && nix fmt"
  echo "${apply_args[*]} ${overrides[*]}"
  exit 0
fi

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
