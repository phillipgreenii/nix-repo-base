#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# WORKSPACE_ROOT is the parent dir; the resolver uses it to find the
# on-disk lib (this repo's own copy) when present.
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
-h | --help)
  echo "Usage: $0 [--ci]"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  exit 1
  ;;
esac

# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
# Pass WORKSPACE_ROOT so the resolver can prefer the on-disk sibling when present.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-repo-base" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_finalize
