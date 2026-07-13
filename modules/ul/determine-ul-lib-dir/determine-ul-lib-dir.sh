# shellcheck shell=bash
# determine-ul-lib-dir: print the directory containing update-locks-lib.bash.
#
# Precedence:
#   1. UL_LIB_DIR_OVERRIDE (highest — operator escape hatch)
#   2. WORKSPACE_ROOT-relative sibling if the file exists AND
#      UL_IGNORE_WORKSPACE_ROOT is unset
#   3. UL_LIB_PACKAGE_PATH (injected at build time by mkBashScript's `config`)
#
# Consumers invoke this via:
#   UL_LIB_DIR=$(nix run github:phillipgreenii/nix-repo-base#determine-ul-lib-dir)
#
# WORKSPACE_ROOT must be exported by the caller for the sibling check to fire.

show_help() {
  cat <<'HELP'
determine-ul-lib-dir: Print the resolved path containing update-locks-lib.bash.

Usage: determine-ul-lib-dir [-h|--help]

Reads env vars in this precedence order and prints the chosen directory:
  UL_LIB_DIR_OVERRIDE             — absolute path; if set, used directly.
  WORKSPACE_ROOT (+ sibling file) — used if the sibling update-locks-lib.bash
                                     exists AND UL_IGNORE_WORKSPACE_ROOT is unset.
  UL_LIB_PACKAGE_PATH             — baked-in nix-store fallback (always defined).

Output is a single line on stdout containing the resolved directory.
HELP
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n ${UL_LIB_DIR_OVERRIDE:-} ]]; then
  echo "$UL_LIB_DIR_OVERRIDE"
  exit 0
fi

if [[ -z ${UL_IGNORE_WORKSPACE_ROOT:-} &&
  -n ${WORKSPACE_ROOT:-} &&
  -f "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash" ]]; then
  echo "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts"
  exit 0
fi

echo "$UL_LIB_PACKAGE_PATH"
