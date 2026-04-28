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

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/scripts/update-locks-lib.bash"
ul_setup "phillipgreenii-nix-repo-base" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_finalize
