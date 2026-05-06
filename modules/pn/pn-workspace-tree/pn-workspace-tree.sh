# shellcheck shell=bash
# pn-workspace-tree: Print ASCII dependency tree of workspace flake repos

_root_arg=""
_all_inputs=false

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-tree: Print ASCII dependency tree of workspace flake repos

Purpose: Displays the flake input dependency graph for the workspace,
rooted at the terminal flake (the repo with no inputName in
pn-workspace.lock). By default shows only workspace-internal deps.

Usage: pn-workspace-tree [OPTIONS]

Options:
  -h, --help        Show this help message and exit
  --root <dir>      Workspace root directory.
                    Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --all-inputs      Show all flake inputs, not just workspace-internal deps.

Example:
  pn-workspace-tree
  pn-workspace-tree --all-inputs
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
  --all-inputs)
    _all_inputs=true
    shift
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n $_root_arg ]] && [[ ! -d $_root_arg ]]; then
  echo "error: --root directory does not exist: $_root_arg" >&2
  exit 1
fi

PN_WORKSPACE_ROOT=$(workspace_resolve_root "$_root_arg") || exit 1
workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT") || exit 1

terminal_count=$(echo "$workspace_json" | jq '[.[] | select(has("inputName") | not)] | length')
if [[ $terminal_count -eq 0 ]]; then
  echo "error: no terminal flake found (all workspace projects have inputName)" >&2
  exit 1
fi
if [[ $terminal_count -gt 1 ]]; then
  terminal_names=$(echo "$workspace_json" |
    jq -r '[.[] | select(has("inputName") | not) | .path | split("/") | .[-1]] | join(", ")')
  echo "error: multiple terminal flakes: $terminal_names" >&2
  exit 1
fi

_TERMINAL_PATH=$(echo "$workspace_json" | jq -r '[.[] | select(has("inputName") | not)] | .[0].path')

_LOCKFILE="$_TERMINAL_PATH/flake.lock"
if [[ ! -f $_LOCKFILE ]]; then
  echo "info: generating flake.lock for $(basename "$_TERMINAL_PATH")" >&2
  if ! nix flake lock "path:$_TERMINAL_PATH"; then
    echo "error: failed to generate flake.lock: $_LOCKFILE" >&2
    exit 1
  fi
fi
_LOCK_JSON=$(cat "$_LOCKFILE")
