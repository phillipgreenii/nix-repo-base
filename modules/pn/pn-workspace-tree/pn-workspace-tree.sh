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

# ─── Global state ─────────────────────────────────────────────────────────────

# JSON array of workspace inputNames (non-terminal repos only)
_WS_INPUT_NAMES=$(echo "$workspace_json" |
  jq -c '[.[] | select(has("inputName")) | .inputName]')

# JSON object: inputName -> repo dir basename (display name)
_WS_DISPLAY_NAMES=$(echo "$workspace_json" | jq -c '
  [.[] | select(has("inputName")) |
    {key: .inputName, value: (.path | split("/") | .[-1])}]
  | from_entries
')

# Warn for workspace repos whose inputName is absent from the lock
while IFS= read -r input_name; do
  [[ -z $input_name ]] && continue
  if ! jq -e --arg k "$input_name" '.nodes[$k] != null' <<<"$_LOCK_JSON" >/dev/null 2>&1; then
    echo "warning: workspace input '$input_name' not in flake.lock; skipping" >&2
  fi
done < <(echo "$_WS_INPUT_NAMES" | jq -r '.[]')

# Visited associative array for dedup (keyed by node key)
declare -A _TREE_VISITED=()

# ─── Adjacency map ────────────────────────────────────────────────────────────

# Print dep node keys for node_key, one per line, sorted by display name.
# "root" = terminal flake root node; any other value = workspace inputName.
_tree_deps() {
  local node_key="$1"
  local inputs_json
  if [[ $node_key == "root" ]]; then
    inputs_json=$(jq -c '.nodes.root.inputs // {}' <<<"$_LOCK_JSON")
  else
    inputs_json=$(jq -c --arg k "$node_key" '.nodes[$k].inputs // {}' <<<"$_LOCK_JSON")
  fi

  # Resolve input values to target node keys:
  #   "string"   -> direct dep (unfollowed copy)
  #   ["X"]      -> follows top-level node X (single-element = direct dep)
  #   ["X","Y"]  -> sub-input follow; NOT a direct dep of this node, skip
  local -a resolved=()
  while IFS= read -r dep; do
    [[ -z $dep ]] && continue
    if [[ $_all_inputs == "false" ]]; then
      jq -e --arg d "$dep" 'index($d) != null' <<<"$_WS_INPUT_NAMES" >/dev/null 2>&1 || continue
    fi
    resolved+=("$dep")
  done < <(jq -r '
    to_entries[] |
    .value as $v |
    if   ($v | type) == "string"                            then $v
    elif ($v | type) == "array" and ($v | length) == 1     then $v[0]
    else empty
    end
  ' <<<"$inputs_json")

  [[ ${#resolved[@]} -eq 0 ]] && return 0

  # Sort by display name: emit "display<TAB>key", sort, strip display
  local -a pairs=()
  local dep display
  for dep in "${resolved[@]}"; do
    display=$(jq -r --arg k "$dep" '.[$k] // $k' <<<"$_WS_DISPLAY_NAMES")
    pairs+=("${display}"$'\t'"${dep}")
  done
  printf '%s\n' "${pairs[@]}" | sort | cut -f2
}

# ─── Tree renderer ────────────────────────────────────────────────────────────

# render_tree <node_key> [<prefix>] [<is_last>]
#   node_key : "root" for the terminal flake, otherwise a lock node key
#   prefix   : string prepended before the connector (blank for root's children)
#   is_last  : "true" if this node is the last sibling (uses └── vs ├──)
render_tree() {
  local node_key="$1"
  local prefix="${2:-}"
  local is_last="${3:-true}"

  local display
  if [[ $node_key == "root" ]]; then
    display=$(basename "$_TERMINAL_PATH")
  else
    display=$(jq -r --arg k "$node_key" '.[$k] // $k' <<<"$_WS_DISPLAY_NAMES")
  fi

  if [[ $node_key == "root" ]]; then
    echo "$display"
  else
    local connector
    [[ $is_last == "true" ]] && connector="└── " || connector="├── "
    if [[ -n ${_TREE_VISITED[$node_key]+x} ]]; then
      echo "${prefix}${connector}${display} [↑ shown above]"
      return
    fi
    echo "${prefix}${connector}${display}"
  fi

  _TREE_VISITED[$node_key]=1

  local -a children=()
  while IFS= read -r child; do
    [[ -n $child ]] && children+=("$child")
  done < <(_tree_deps "$node_key")

  local child_prefix
  if [[ $node_key == "root" ]]; then
    child_prefix=""
  elif [[ $is_last == "true" ]]; then
    child_prefix="${prefix}    "
  else
    child_prefix="${prefix}│   "
  fi

  local n=${#children[@]}
  local i
  for ((i = 0; i < n; i++)); do
    local child_is_last="false"
    [[ $i -eq $((n - 1)) ]] && child_is_last="true"
    render_tree "${children[$i]}" "$child_prefix" "$child_is_last"
  done
}

# ─── Main ─────────────────────────────────────────────────────────────────────

render_tree "root"
