# shellcheck shell=bash
# pn-workspace-upgrade: Complete workspace upgrade (update + apply)

if [[ ${1:-} == "--help" || ${1:-} == "-h" ]]; then
  cat <<'HELP'
pn-workspace-upgrade: Complete workspace upgrade

Purpose: This is the one-command solution for a full workspace upgrade. For each
project in dependency order, it pulls the latest changes, updates flake
dependencies, and pushes. Then applies the configuration to the local system.
Combines pn-workspace-update (which handles pull and push per project) and
pn-workspace-apply.

Usage: pn-workspace-upgrade [OPTIONS]

Options:
  -h, --help     Show this help message and exit

Example:
  # Complete workspace upgrade (update + apply)
  pn-workspace-upgrade
HELP
  exit 0
fi

pn-workspace-update && pn-workspace-apply
