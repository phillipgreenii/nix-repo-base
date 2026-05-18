# shellcheck shell=bash
# pn-workspace-flake-check: Run `nix flake check` for all workspace repos via pn-ws-nix

_root_arg=""
_workspace_arg=""
_override_specs=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-flake-check: Run `nix flake check` for all workspace repos

Purpose: Runs `nix flake check` (via pn-ws-nix, so overrides are injected
automatically) for every repo declared in the nearest pn-workspace.toml.
Searches ancestor directories from the current working directory to find the
workspace root. Continues past per-project failures (full sweep); overall
exit code is non-zero if any project failed.

Usage: pn-workspace-flake-check [OPTIONS]

Options:
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Checks run in the swapped path.
                                Also accepts PN_WORKSPACE_OVERRIDE_PATHS env var
                                with comma-separated entries.

Example:
  # Run flake check across all workspace repos
  pn-workspace-flake-check
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

declare -a _failed=()
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Flake check $project_name ==--  "
  if ! (cd "$project_path" && pn-ws-nix flake check); then
    _failed+=("$project_name")
  fi
  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')

if [[ ${#_failed[@]} -gt 0 ]]; then
  echo "FAIL: ${#_failed[@]} project(s) failed flake check: ${_failed[*]}" >&2
  exit 1
fi

echo "OK: all projects passed flake check"
