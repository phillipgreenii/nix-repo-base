# shellcheck shell=bash
# pn-ws-nix: workspace-aware nix wrapper that injects --override-input
# for every project declared in pn-workspace.toml / pn-workspace.lock.

_action_arg=""
_remaining_args=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-ws-nix: Workspace-aware nix wrapper that injects --override-input

Purpose: Runs `nix <subcommand>` with --override-input flags pointing at the
local working copy of every project declared in the nearest pn-workspace.toml.
Searches ancestor directories from the current working directory to find the
workspace root (honors PN_WORKSPACE_ROOT env var).

Usage: pn-ws-nix [--non-override-subcommand-action {error|warn|ignore}] <nix-args...>

Options:
  --non-override-subcommand-action {error|warn|ignore}
                          Behavior when the nix subcommand is one for which
                          overrides do not apply (currently: `flake update`
                          and `flake lock`).
                            error  -> print message to stderr, exit 2
                            warn   -> print message to stderr, exec nix without overrides (default)
                            ignore -> exec nix without overrides, silently
                          Honors PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION env var.
                          Flag takes priority over env var.

Examples:
  # Run flake check on the current project with workspace overrides
  pn-ws-nix flake check

  # Build a single package with workspace overrides
  pn-ws-nix build .#my-package

  # Update the lock (skips override injection automatically)
  pn-ws-nix flake update

For non-flake nix subcommands (`store *`, `profile list`, `log`, etc.), use
bare `nix` directly; --override-input does not apply to those.
HELP
    exit 0
    ;;
  --non-override-subcommand-action)
    _action_arg="$2"
    shift 2
    ;;
  --non-override-subcommand-action=*)
    _action_arg="${1#*=}"
    shift
    ;;
  *)
    _remaining_args+=("$1")
    shift
    ;;
  esac
done

# Resolve action: flag > env var > default
_action="${_action_arg:-${PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION:-warn}}"

case "$_action" in
error | warn | ignore) ;;
*)
  echo "error: invalid --non-override-subcommand-action value: $_action (allowed: error, warn, ignore)" >&2
  exit 2
  ;;
esac

if [[ ${#_remaining_args[@]} -eq 0 ]]; then
  echo "error: pn-ws-nix requires at least one nix argument; try 'pn-ws-nix --help'" >&2
  exit 2
fi

# Identify subcommand: first non-flag arg, plus the second if first is "flake"
_subcommand="${_remaining_args[0]}"
if [[ $_subcommand == "flake" && ${#_remaining_args[@]} -ge 2 ]]; then
  _subcommand="flake ${_remaining_args[1]}"
fi

# Deny-list: nix subcommands where --override-input is silently ignored.
_is_deny_listed() {
  case "$1" in
  "flake update" | "flake lock") return 0 ;;
  *) return 1 ;;
  esac
}

if _is_deny_listed "$_subcommand"; then
  case "$_action" in
  error)
    echo "pn-ws-nix: overrides not applicable to '$_subcommand'. Run \`nix ${_remaining_args[*]}\` directly if intentional." >&2
    exit 2
    ;;
  warn)
    echo "pn-ws-nix: overrides not applicable to '$_subcommand'. Running nix without overrides; use bare \`nix\` directly to silence this." >&2
    ;;
  ignore) ;;
  esac
  exec nix "${_remaining_args[@]}"
fi

# Resolve workspace root + build override flags
PN_WORKSPACE_ROOT=$(workspace_resolve_root "") || exit 1

overrides_json=$(workspace_parse_overrides) || exit 1
workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

overrides=()
while IFS= read -r entry; do
  path=$(echo "$entry" | jq -r '.path')
  input_name=$(echo "$entry" | jq -r '.inputName')
  overrides+=(--override-input "$input_name" "git+file://$path")
done < <(echo "$workspace_json" | jq -c '.[] | select(.inputName != null)')

exec nix "${_remaining_args[@]}" "${overrides[@]}"
