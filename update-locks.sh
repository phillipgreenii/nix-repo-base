#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

# base IS nix-repo-base — source the in-tree lib directly; never self-fetch the
# resolver over the network (that would build+run whatever is at GitHub HEAD in
# token-bearing CI, the unpinned-HEAD code-execution hole this closes).
UL_LIB_DIR="${UL_LIB_DIR:-${SCRIPT_DIR}/lib/scripts}"
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-repo-base" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_finalize
