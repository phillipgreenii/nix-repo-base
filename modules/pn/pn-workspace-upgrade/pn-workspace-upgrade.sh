# shellcheck shell=bash
# pn-workspace-upgrade: Complete workspace upgrade (update + apply)

_workspace_arg=""
_apply_cmd_arg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-upgrade: Complete workspace upgrade

Purpose: This is the one-command solution for a full workspace upgrade. For each
project in dependency order, it pulls the latest changes, updates flake
dependencies, and pushes. Then applies the configuration to the local system.
Combines pn-workspace-update (which handles pull and push per project) and
pn-workspace-apply.

Usage: pn-workspace-upgrade [OPTIONS]

Options:
  -h, --help                  Show this help message and exit
  --workspace <dir>           Workspace root directory (default: walk up from CWD)
  --apply-cmd <template>      Override apply_command from pn-workspace.toml (forwarded to pn-workspace-apply).
                              Supports {terminal_flake} and {hostname} placeholders.

Example:
  # Complete workspace upgrade (update + apply)
  pn-workspace-upgrade
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
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

update_args=()
apply_args=()

if [[ -n $_workspace_arg ]]; then
  update_args+=(--workspace "$_workspace_arg")
  apply_args+=(--workspace "$_workspace_arg")
fi

if [[ -n $_apply_cmd_arg ]]; then
  apply_args+=(--apply-cmd "$_apply_cmd_arg")
fi

pn-workspace-update "${update_args[@]}" && pn-workspace-apply "${apply_args[@]}"
